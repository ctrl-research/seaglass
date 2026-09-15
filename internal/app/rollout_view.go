package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// rolloutPoll is how often the rollout view refetches the object.
const rolloutPoll = time.Second

type rolloutTickMsg struct{ id int }

type rolloutStatusMsg struct {
	id    int
	state k8s.RolloutState
	err   error
}

var (
	rolloutOKStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true)
	rolloutFailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	rolloutWaitStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	rolloutLogStyle  = lipgloss.NewStyle().Faint(true)
)

// rolloutView follows a workload's rollout, polling until it completes or
// fails. It shows a running log of distinct status messages.
type rolloutView struct {
	id        int
	res       k8s.Resource
	namespace string
	name      string

	deps    deps
	cancel  context.CancelFunc
	spin    spinner.Model
	log     []string
	state   k8s.RolloutState
	started time.Time
	done    bool
	err     error

	width, height int
}

func newRolloutView(id int, res k8s.Resource, ns, name string) *rolloutView {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return &rolloutView{id: id, res: res, namespace: ns, name: name, spin: sp, started: time.Now()}
}

func (v *rolloutView) start(d deps) tea.Cmd {
	v.deps = d
	_, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	return tea.Batch(v.spin.Tick, v.poll())
}

func (v *rolloutView) stop() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
}

// poll fetches the object once and reports its rollout state.
func (v *rolloutView) poll() tea.Cmd {
	id, res, ns, name, get := v.id, v.res, v.namespace, v.name, v.deps.get
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		obj, err := get.Get(ctx, res, ns, name)
		if err != nil {
			return rolloutStatusMsg{id: id, err: err}
		}
		return rolloutStatusMsg{id: id, state: k8s.RolloutStatus(obj)}
	}
}

// handleStatus records a state and schedules the next poll unless finished.
func (v *rolloutView) handleStatus(msg rolloutStatusMsg) tea.Cmd {
	if msg.err != nil {
		v.err = msg.err
		v.done = true
		return nil
	}
	v.state = msg.state
	// Append only when the message changes, so the log reads as progress.
	if len(v.log) == 0 || v.log[len(v.log)-1] != msg.state.Message {
		v.log = append(v.log, msg.state.Message)
	}
	if msg.state.Done || msg.state.Failed {
		v.done = true
		v.stop()
		return nil
	}
	return tea.Tick(rolloutPoll, func(time.Time) tea.Msg { return rolloutTickMsg{id: v.id} })
}

func (v *rolloutView) handleTick(msg rolloutTickMsg) tea.Cmd {
	if v.done || msg.id != v.id {
		return nil
	}
	return v.poll()
}

func (v *rolloutView) handleSpinner(msg tea.Msg) tea.Cmd {
	if v.done {
		return nil
	}
	var cmd tea.Cmd
	v.spin, cmd = v.spin.Update(msg)
	return cmd
}

func (v *rolloutView) resize(width, height int) { v.width, v.height = width, height }

func (v *rolloutView) handleKey(tea.KeyPressMsg, int, int) (tea.Cmd, bool) {
	return nil, false // all keys (esc to leave) are global
}

func (v *rolloutView) render(width, height int) string {
	var b strings.Builder
	title := v.res.Kind + " " + v.name
	if v.namespace != "" {
		title = v.res.Kind + " " + v.namespace + "/" + v.name
	}
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Rollout status: "+title) + "\n\n")

	for i, line := range v.log {
		mark := rolloutLogStyle.Render("  · ")
		if i == len(v.log)-1 && !v.done {
			mark = "  " + v.spin.View() + " "
		}
		b.WriteString(mark + rolloutLogStyle.Render(line) + "\n")
	}
	b.WriteString("\n")
	switch {
	case v.err != nil:
		b.WriteString(rolloutFailStyle.Render("error: " + v.err.Error()))
	case v.state.Failed:
		b.WriteString(rolloutFailStyle.Render("✗ rollout failed"))
	case v.state.Done:
		b.WriteString(rolloutOKStyle.Render("✓ rollout complete") + rolloutLogStyle.Render(fmt.Sprintf("  (%s)", elapsed(v.started))))
	default:
		b.WriteString(rolloutWaitStyle.Render("… rolling out") + rolloutLogStyle.Render(fmt.Sprintf("  (%s)", elapsed(v.started))))
	}
	body := lipgloss.NewStyle().Padding(1, 2).Render(b.String())
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
}

func elapsed(since time.Time) string {
	d := time.Since(since).Round(time.Second)
	return d.String()
}

func (v *rolloutView) crumbs() []string { return []string{v.res.Name(), v.name, "rollout"} }

func (v *rolloutView) capturesInput() bool { return false }

func (v *rolloutView) status() viewStatus {
	state := "rolling out"
	switch {
	case v.err != nil:
		state = "error"
	case v.state.Failed:
		state = "failed"
	case v.state.Done:
		state = "complete"
	}
	return viewStatus{state: state, err: v.err}
}

func (v *rolloutView) hint() string { return "esc back" }

func (v *rolloutView) help() []helpSection {
	return []helpSection{{"Rollout", []key.Binding{keys.Back}}}
}
