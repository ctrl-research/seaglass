package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// execer is the slice of k8s.Client the shell view needs.
type execer interface {
	Exec(ctx context.Context, o k8s.ExecOptions) error
	RunCommand(ctx context.Context, namespace, pod, container string, command []string) (string, error)
}

type (
	// shellOutputMsg carries remote output for the view with matching id.
	shellOutputMsg struct {
		id   int
		data [][]byte
	}
	// shellExitMsg reports the end of the session.
	shellExitMsg struct {
		id  int
		err error
	}
	// shellUserMsg carries the remote user name.
	shellUserMsg struct {
		id   int
		user string
	}
)

var shellEndStyle = lipgloss.NewStyle().Faint(true).Italic(true)

// shellView is an interactive shell in a container, rendered inside the
// TUI by a terminal emulator. All keys go to the shell until it exits or
// the user presses the close key.
type shellView struct {
	id        int
	namespace string
	pod       string
	container string
	user      string

	em     *vt.Emulator
	out    chan []byte
	exit   chan error
	sizes  *sizeQueue
	cancel context.CancelFunc

	started bool
	done    bool
	err     error

	width, height int
}

func newShellView(id int, ns, pod, container string) *shellView {
	return &shellView{id: id, namespace: ns, pod: pod, container: container, width: 80, height: 24}
}

// start opens the session and probes the remote user.
func (v *shellView) start(d deps) tea.Cmd {
	if v.started {
		return nil // a shell cannot be resumed; the ended view stays for esc
	}
	v.started = true
	v.em = vt.NewEmulator(max(v.width, 2), max(v.height, 1))
	v.out = make(chan []byte, 1024)
	v.exit = make(chan error, 1)
	v.sizes = newSizeQueue(v.width, v.height)
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel

	w := &chanWriter{ch: v.out}
	opts := k8s.ExecOptions{
		Namespace: v.namespace, Pod: v.pod, Container: v.container,
		Stdin: v.em, Stdout: w, Stderr: w, TTY: true, Sizes: v.sizes,
	}
	go func() {
		v.exit <- d.exec.Exec(ctx, opts)
	}()

	id, ns, pod, container := v.id, v.namespace, v.pod, v.container
	probe := func() tea.Msg {
		// Try id, then common env vars, then the numeric uid. Distroless
		// images may have none of these, in which case the user stays "?".
		script := `id -un 2>/dev/null || printf '%s' "${USER:-${LOGNAME:-}}" || true; [ -z "${USER:-${LOGNAME:-}}" ] && id -u 2>/dev/null | sed 's/^/uid /' || true`
		u, err := d.exec.RunCommand(ctx, ns, pod, container, []string{"/bin/sh", "-c", script})
		if err != nil || strings.TrimSpace(u) == "" {
			return nil
		}
		return shellUserMsg{id: id, user: strings.TrimSpace(u)}
	}
	return tea.Batch(v.wait(), probe)
}

func (v *shellView) stop() {
	if v.cancel != nil {
		v.cancel()
	}
	if v.sizes != nil {
		v.sizes.stop()
	}
	// Close only the emulator's input pipe: the remote session's stdin
	// copier is blocked reading it and exits on EOF. Emulator.Close would
	// race with that Read on an unguarded flag.
	if v.em != nil {
		if c, ok := v.em.InputPipe().(io.Closer); ok {
			_ = c.Close()
		}
	}
}

// wait delivers the next burst of output or the exit.
func (v *shellView) wait() tea.Cmd {
	out, exit, id := v.out, v.exit, v.id
	return func() tea.Msg {
		select {
		case err := <-exit:
			return shellExitMsg{id: id, err: err}
		case first := <-out:
			data := [][]byte{first}
			for len(data) < 256 {
				select {
				case b := <-out:
					data = append(data, b)
				default:
					return shellOutputMsg{id: id, data: data}
				}
			}
			return shellOutputMsg{id: id, data: data}
		}
	}
}

func (v *shellView) handleOutput(msg shellOutputMsg) tea.Cmd {
	for _, b := range msg.data {
		_, _ = v.em.Write(b)
	}
	return v.wait()
}

func (v *shellView) handleExit(msg shellExitMsg) {
	v.done = true
	v.err = msg.err
	if v.sizes != nil {
		v.sizes.stop()
	}
	if v.cancel != nil {
		v.cancel()
	}
}

func (v *shellView) resize(width, height int) {
	v.width, v.height = width, height
	if v.em != nil && !v.done {
		v.em.Resize(max(width, 2), max(height, 1))
		v.sizes.push(width, height)
	}
}

// capturesInput is true while the session runs: every key belongs to the
// shell. After exit, normal navigation resumes.
func (v *shellView) capturesInput() bool { return v.started && !v.done }

func (v *shellView) handleKey(msg tea.KeyPressMsg, _, _ int) (tea.Cmd, bool) {
	if v.done {
		return nil, false
	}
	if is(msg, keys.ShellClose) {
		v.stop()
		return nil, true
	}
	v.em.SendKey(uv.KeyPressEvent{
		Text: msg.Text, Mod: uv.KeyMod(msg.Mod), Code: msg.Code,
		ShiftedCode: msg.ShiftedCode, BaseCode: msg.BaseCode, IsRepeat: msg.IsRepeat,
	})
	return nil, true
}

// cursor is the shell cursor position within the body, or nil.
func (v *shellView) cursor() *uv.Position {
	if v.em == nil || v.done {
		return nil
	}
	p := v.em.CursorPosition()
	return &p
}

func (v *shellView) render(width, height int) string {
	if v.em == nil {
		return lipgloss.NewStyle().Height(height).Render("")
	}
	body := v.em.Render()
	if v.done {
		msg := "── shell exited · esc to go back ──"
		if v.err != nil {
			msg = "── " + v.err.Error() + " · esc to go back ──"
		}
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		if len(lines) >= height {
			lines = lines[:height-1]
		}
		body = strings.Join(lines, "\n") + "\n" + shellEndStyle.Render(msg)
	}
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
}

func (v *shellView) crumbs() []string {
	return []string{k8s.Pods.Name(), v.pod, "shell: " + v.container}
}

func (v *shellView) status() viewStatus {
	who := "user ?"
	if v.user != "" {
		who = "user " + v.user
	}
	state := who + " · " + fmt.Sprintf("%s/%s", v.namespace, v.pod)
	if v.done {
		state = "exited · " + state
	}
	return viewStatus{state: state}
}

func (v *shellView) hint() string {
	if v.done {
		return "esc back"
	}
	return "keys go to the shell · " + keys.ShellClose.Help().Key + " closes it"
}

func (v *shellView) help() []helpSection {
	return []helpSection{{"Shell", []key.Binding{keys.ShellClose, bind("exit", "leave the shell normally", "")}}}
}

// chanWriter copies each write onto a channel.
type chanWriter struct{ ch chan []byte }

func (w *chanWriter) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	w.ch <- b
	return len(p), nil
}

// sizeQueue feeds terminal sizes to the remote session.
type sizeQueue struct {
	ch    chan *remotecommand.TerminalSize
	once  sync.Once
	stopc chan struct{}
}

func newSizeQueue(w, h int) *sizeQueue {
	q := &sizeQueue{ch: make(chan *remotecommand.TerminalSize, 4), stopc: make(chan struct{})}
	q.push(w, h)
	return q
}

func (q *sizeQueue) push(w, h int) {
	select {
	case q.ch <- &remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}:
	default:
	}
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case s := <-q.ch:
		return s
	case <-q.stopc:
		return nil
	}
}

func (q *sizeQueue) stop() { q.once.Do(func() { close(q.stopc) }) }
