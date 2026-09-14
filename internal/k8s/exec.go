package k8s

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/tools/remotecommand"
)

// DefaultShell picks bash when the image has it and falls back to sh.
var DefaultShell = []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}

// ExecCommand runs an interactive command in a container over the API
// server, attached to the local terminal. It satisfies Bubble Tea's
// ExecCommand so the program can hand the terminal over and take it back.
type ExecCommand struct {
	client    *Client
	namespace string
	pod       string
	container string
	command   []string

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// Shell returns an ExecCommand for an interactive shell in a container.
func (c *Client) Shell(namespace, pod, container string, command []string) *ExecCommand {
	if len(command) == 0 {
		command = DefaultShell
	}
	return &ExecCommand{client: c, namespace: namespace, pod: pod, container: container, command: command,
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
}

func (e *ExecCommand) SetStdin(r io.Reader)  { e.stdin = r }
func (e *ExecCommand) SetStdout(w io.Writer) { e.stdout = w }
func (e *ExecCommand) SetStderr(w io.Writer) { e.stderr = w }

// Run streams the session until the remote process exits. The local
// terminal is put in raw mode for the duration and restored after.
func (e *ExecCommand) Run() error {
	req := e.client.rest.Post().
		AbsPath(append(resourcePath(Pods.GVR, e.namespace), e.pod, "exec")...).
		Param("container", e.container).
		Param("stdin", "true").
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "true")
	for _, arg := range e.command {
		req = req.Param("command", arg)
	}
	url := req.URL()

	spdy, err := remotecommand.NewSPDYExecutor(e.client.cfg, "POST", url)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	ws, err := remotecommand.NewWebSocketExecutor(e.client.cfg, "POST", url.String())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	// The remote command copies stdin in its own goroutine, which stays
	// blocked in read(2) after the session ends and would swallow the next
	// keystroke meant for the TUI. A cancelable reader lets us unblock it
	// without consuming any bytes.
	stdin := e.stdin
	cr, err := cancelreader.NewReader(e.stdin)
	if err == nil {
		stdin = cr
		defer func() {
			cr.Cancel()
			cr.Close()
		}()
	}

	var sizeQueue remotecommand.TerminalSizeQueue
	if f, ok := e.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fd := int(f.Fd())
		state, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("raw mode: %w", err)
		}
		defer term.Restore(fd, state) //nolint:errcheck
		q := newSizeQueue(fd)
		defer q.stop()
		sizeQueue = q
	}

	err = exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{
		Stdin:             stdin,
		Stdout:            e.stdout,
		Stderr:            e.stderr,
		Tty:               true,
		TerminalSizeQueue: sizeQueue,
	})
	if err != nil {
		return fmt.Errorf("exec %s/%s[%s]: %w", e.namespace, e.pod, e.container, err)
	}
	return nil
}

// sizeQueue reports the initial terminal size and then every SIGWINCH.
type sizeQueue struct {
	sizes chan *remotecommand.TerminalSize
	stopc chan struct{}
}

func newSizeQueue(fd int) *sizeQueue {
	q := &sizeQueue{sizes: make(chan *remotecommand.TerminalSize, 1), stopc: make(chan struct{})}
	q.push(fd)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case <-sig:
				q.push(fd)
			case <-q.stopc:
				return
			}
		}
	}()
	return q
}

func (q *sizeQueue) push(fd int) {
	w, h, err := term.GetSize(fd)
	if err != nil {
		return
	}
	select {
	case q.sizes <- &remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}:
	default:
	}
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case s := <-q.sizes:
		return s
	case <-q.stopc:
		return nil
	}
}

func (q *sizeQueue) stop() { close(q.stopc) }
