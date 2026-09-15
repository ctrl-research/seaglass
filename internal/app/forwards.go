package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// forwarder is the slice of k8s.Client the forward flow needs.
type forwarder interface {
	ForwardPort(namespace, pod string, local, remote uint16) (*k8s.PortForward, error)
}

// activeForward is one running port-forward tracked by the model.
type activeForward struct {
	id     int
	pf     *k8s.PortForward
	res    k8s.Resource
	label  string // "ns/pod"
	local  uint16
	remote uint16
	cancel context.CancelFunc
}

// forwards is the model's registry of running forwards, shared with the
// forwards view. It is only mutated on the UI goroutine.
type forwards struct {
	list   []*activeForward
	nextID int
}

func (f *forwards) count() int { return len(f.list) }

func (f *forwards) add(a *activeForward) { f.list = append(f.list, a) }

func (f *forwards) remove(id int) *activeForward {
	for i, a := range f.list {
		if a.id == id {
			f.list = append(f.list[:i], f.list[i+1:]...)
			return a
		}
	}
	return nil
}

type (
	// fwdContainersMsg carries a pod's container ports for a pending forward.
	fwdContainersMsg struct {
		tgt   target
		ports []uint16
		err   error
	}
	// forwardStartedMsg reports a forward that came up (or failed to).
	forwardStartedMsg struct {
		af  *activeForward
		err error
	}
	// forwardDiedMsg reports a forward that stopped on its own.
	forwardDiedMsg struct {
		id  int
		err error
	}
)

// handleFwdContainers decides how to gather the remote port for a forward:
// straight through for a single declared port, a picker for several, or a
// prompt when the pod declares none.
func (m Model) handleFwdContainers(msg fwdContainersMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	switch len(msg.ports) {
	case 1:
		return m, startForward(m.deps.fwd, msg.tgt.res, msg.tgt.namespace, msg.tgt.name, 0, msg.ports[0])
	case 0:
		tgt := msg.tgt
		m.fwdTarget = &tgt
		a := action{Name: "port-forward", Desc: "forward a port", Input: &inputSpec{Label: "remote port", Validate: validatePort}}
		pa := pendingAction{act: a, tgt: tgt}
		cmd := m.prompt.show(pa, "")
		m.top().resize(m.width, m.bodyHeight())
		return m, cmd
	default:
		tgt := msg.tgt
		m.fwdTarget = &tgt
		cmd := m.palette.showWith(portItems(msg.ports), "forward which port?")
		m.top().resize(m.width, m.bodyHeight())
		return m, cmd
	}
}

// openForwards pushes the forwards panel.
func (m *Model) openForwards() {
	v := newForwardsView(&m.forwards)
	m.top().stop()
	m.stack = append(m.stack, v)
	v.resize(m.width, m.bodyHeight())
}

// startForward brings up a forward off the UI thread.
func startForward(fwd forwarder, res k8s.Resource, ns, pod string, local, remote uint16) tea.Cmd {
	return func() tea.Msg {
		pf, err := fwd.ForwardPort(ns, pod, local, remote)
		if err != nil {
			return forwardStartedMsg{err: err}
		}
		return forwardStartedMsg{af: &activeForward{
			pf: pf, res: res, label: ns + "/" + pod, local: pf.Local, remote: remote,
		}}
	}
}

// watchForward emits a message when a forward dies.
func watchForward(af *activeForward) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	af.cancel = cancel
	return func() tea.Msg {
		err := af.pf.Wait(ctx)
		if ctx.Err() != nil {
			return nil // cancelled by us; the view already dropped it
		}
		return forwardDiedMsg{id: af.id, err: err}
	}
}

// forwardsView lists the running forwards and lets you cancel them.
type forwardsView struct {
	reg           *forwards
	cursor        int
	width, height int
}

func newForwardsView(reg *forwards) *forwardsView { return &forwardsView{reg: reg} }

func (v *forwardsView) start(deps) tea.Cmd { return nil }
func (v *forwardsView) stop()              {}

func (v *forwardsView) resize(width, height int) { v.width, v.height = width, height }

func (v *forwardsView) clampCursor() {
	if v.cursor >= len(v.reg.list) {
		v.cursor = len(v.reg.list) - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

// cancelSelected returns the id to stop, or -1.
func (v *forwardsView) selected() *activeForward {
	v.clampCursor()
	if v.cursor < 0 || v.cursor >= len(v.reg.list) {
		return nil
	}
	return v.reg.list[v.cursor]
}

func (v *forwardsView) handleKey(msg tea.KeyPressMsg, _, _ int) (tea.Cmd, bool) {
	switch {
	case is(msg, keys.Up):
		v.cursor--
		v.clampCursor()
		return nil, true
	case is(msg, keys.Down):
		v.cursor++
		v.clampCursor()
		return nil, true
	case is(msg, keys.Delete), is(msg, keys.Accept), is(msg, keys.Back):
		// Cancel and back are handled by the model, which owns the registry
		// and the watch goroutines.
		return nil, false
	}
	return nil, true
}

var (
	fwdArrowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("79"))
	fwdAddrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	fwdSelStyle   = lipgloss.NewStyle().Background(lipgloss.Color("57")).Foreground(lipgloss.Color("229")).Bold(true)
	fwdEmptyStyle = lipgloss.NewStyle().Faint(true).Italic(true).Padding(1, 2)
)

func (v *forwardsView) render(width, height int) string {
	if len(v.reg.list) == 0 {
		body := fwdEmptyStyle.Render("no active port-forwards\n\nselect a pod and press F to start one")
		return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
	}
	v.clampCursor()
	rows := make([]*activeForward, len(v.reg.list))
	copy(rows, v.reg.list)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].id < rows[j].id })

	addrW := 0
	for _, a := range rows {
		addrW = max(addrW, len(a.pf.Addr()))
	}
	var lines []string
	for _, a := range rows {
		addr := fmt.Sprintf("%-*s", addrW, a.pf.Addr())
		target := a.label + ":" + strconv.Itoa(int(a.remote))
		if fwdSelected(v, a) {
			lines = append(lines, fwdSelStyle.Width(width).Render(fmt.Sprintf("  %s → %s", addr, target)))
		} else {
			lines = append(lines, "  "+fwdAddrStyle.Render(addr)+" "+fwdArrowStyle.Render("→")+" "+target)
		}
	}
	body := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
}

func fwdSelected(v *forwardsView, a *activeForward) bool {
	sel := v.selected()
	return sel != nil && sel.id == a.id
}

func (v *forwardsView) crumbs() []string { return []string{"port-forwards"} }

func (v *forwardsView) capturesInput() bool { return false }

func (v *forwardsView) status() viewStatus {
	return viewStatus{count: fmt.Sprintf("%d active", len(v.reg.list))}
}

func (v *forwardsView) hint() string { return "enter/ctrl+d cancel  ↑↓ move" }

func (v *forwardsView) help() []helpSection {
	return []helpSection{{"Port-forwards", []key.Binding{keys.Up, keys.Down, keys.Delete, keys.Back}}}
}

// portForwardPrompt validates a remote port.
func validatePort(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be 1-65535")
	}
	return nil
}
