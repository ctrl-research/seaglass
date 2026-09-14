package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/tools/remotecommand"
)

// DefaultShell picks bash when the image has it and falls back to sh.
var DefaultShell = []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}

// ExecOptions describes a remote command session.
type ExecOptions struct {
	Namespace string
	Pod       string
	Container string
	Command   []string // empty means DefaultShell
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	TTY       bool
	// Sizes reports terminal size changes when TTY is set. Optional.
	Sizes remotecommand.TerminalSizeQueue
}

// Exec runs a command in a container and blocks until it exits or ctx
// ends. It uses websockets with an SPDY fallback, like kubectl.
func (c *Client) Exec(ctx context.Context, o ExecOptions) error {
	command := o.Command
	if len(command) == 0 {
		command = DefaultShell
	}
	req := c.rest.Post().
		AbsPath(append(resourcePath(Pods.GVR, o.Namespace), o.Pod, "exec")...).
		Param("container", o.Container).
		Param("stdin", fmt.Sprint(o.Stdin != nil)).
		Param("stdout", "true").
		Param("stderr", fmt.Sprint(!o.TTY)).
		Param("tty", fmt.Sprint(o.TTY))
	for _, arg := range command {
		req = req.Param("command", arg)
	}
	url := req.URL()

	spdy, err := remotecommand.NewSPDYExecutor(c.cfg, "POST", url)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	ws, err := remotecommand.NewWebSocketExecutor(c.cfg, "POST", url.String())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	stderr := o.Stderr
	if o.TTY {
		stderr = nil // merged into stdout by the tty
	}
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             o.Stdin,
		Stdout:            o.Stdout,
		Stderr:            stderr,
		Tty:               o.TTY,
		TerminalSizeQueue: o.Sizes,
	}); err != nil {
		return fmt.Errorf("exec %s/%s[%s]: %w", o.Namespace, o.Pod, o.Container, err)
	}
	return nil
}

// RunCommand runs a non-interactive command and returns its trimmed
// stdout. Used for small probes such as `id -un`.
func (c *Client) RunCommand(ctx context.Context, namespace, pod, container string, command []string) (string, error) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	err := c.Exec(rctx, ExecOptions{
		Namespace: namespace, Pod: pod, Container: container, Command: command,
		Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		if errb.Len() > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
		}
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
