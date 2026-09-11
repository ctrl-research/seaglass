// Package app holds the root Bubble Tea model. Models are pure with respect
// to cluster I/O: everything network-bound runs in internal/k8s goroutines or
// tea.Cmds and arrives here as messages.
package app

import (
	"context"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

// Options configures the root model.
type Options struct {
	Client    *k8s.Client
	Namespace string // "" means all namespaces
	Resource  k8s.Resource

	// streamer overrides the client's stream for tests.
	streamer streamer
	// contexts overrides kubeconfig context discovery for tests.
	contexts []string
}

// Model is the root application model: a navigation stack of resource
// views, a command palette, and the cluster connection.
type Model struct {
	client    *k8s.Client
	stream    streamer
	namespace string

	stack  []*resourceView
	nextID int

	palette    palette
	resources  []k8s.Resource
	namespaces []string
	contexts   []string

	connecting string // context name while switching, "" otherwise
	err        error

	width, height int
}

// Messages from cmds.
type (
	resourcesMsg struct {
		resources []k8s.Resource
		err       error
	}
	namespacesMsg struct {
		names []string
		err   error
	}
	contextsMsg struct {
		names []string
		err   error
	}
	clientMsg struct {
		client *k8s.Client
		err    error
	}
)

// New builds the root model. Streaming starts in Init.
func New(opts Options) Model {
	m := Model{
		client:    opts.Client,
		stream:    opts.streamer,
		namespace: opts.Namespace,
		palette:   newPalette(),
		contexts:  opts.contexts,
	}
	if m.stream == nil && opts.Client != nil {
		m.stream = clientStreamer{opts.Client}
	}
	m.push(opts.Resource)
	return m
}

// Init starts the first stream and kicks off discovery.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.top().start(m.stream)}
	cmds = append(cmds, m.discoverCmds()...)
	if m.contexts == nil {
		cmds = append(cmds, func() tea.Msg {
			names, _, err := k8s.ListContexts()
			return contextsMsg{names: names, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// discoverCmds fetches resource types and namespaces for the palette.
func (m Model) discoverCmds() []tea.Cmd {
	c := m.client
	if c == nil {
		return nil
	}
	return []tea.Cmd{
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rs, err := c.Resources(ctx)
			return resourcesMsg{resources: rs, err: err}
		},
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			names, err := c.NamespaceNames(ctx)
			return namespacesMsg{names: names, err: err}
		},
	}
}

func (m *Model) top() *resourceView { return m.stack[len(m.stack)-1] }

// push adds a view for res on top of the stack. It does not start it.
func (m *Model) push(res k8s.Resource) *resourceView {
	m.nextID++
	v := newResourceView(m.nextID, res, m.namespace)
	m.stack = append(m.stack, v)
	return v
}

// bodyHeight is the height available to the top view.
func (m Model) bodyHeight() int {
	return max(m.height-1-m.palette.height(), 1)
}

func (m *Model) rebuildPalette() {
	m.palette.setItems(buildItems(m.resources, m.namespaces, m.contexts))
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.palette.width = msg.Width
		v := m.top()
		v.layout(m.width, m.bodyHeight(), v.selectedKey())
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case updateMsg:
		v := m.top()
		if msg.id != v.id {
			return m, nil // from a view that was popped or restarted
		}
		return m, v.handle(msg, m.width, m.bodyHeight())

	case resourcesMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.resources = msg.resources
		}
		m.rebuildPalette()
		return m, nil

	case namespacesMsg:
		if msg.err == nil {
			m.namespaces = msg.names
		}
		m.rebuildPalette()
		return m, nil

	case contextsMsg:
		if msg.err == nil {
			m.contexts = msg.names
		}
		m.rebuildPalette()
		return m, nil

	case clientMsg:
		m.connecting = ""
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		return m, m.useClient(msg.client)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	slog.Debug("key", "key", msg.String(), "palette", m.palette.open, "stack", len(m.stack))
	if m.palette.open {
		chosen, closed, cmd := m.palette.update(msg)
		if closed {
			v := m.top()
			v.layout(m.width, m.bodyHeight(), v.selectedKey())
		}
		if chosen != nil {
			return m, tea.Batch(cmd, m.choose(*chosen))
		}
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c", "q":
		for _, v := range m.stack {
			v.stop()
		}
		return m, tea.Quit
	case ":", "ctrl+p":
		cmd := m.palette.show()
		v := m.top()
		v.layout(m.width, m.bodyHeight(), v.selectedKey())
		return m, cmd
	case "esc":
		if len(m.stack) > 1 {
			return m, m.pop()
		}
		m.err = nil
		return m, nil
	}
	return m, m.top().update(msg)
}

// choose acts on a palette selection.
func (m *Model) choose(it paletteItem) tea.Cmd {
	switch it.Kind {
	case itemResource:
		if it.Resource.GVR == m.top().res.GVR {
			return nil
		}
		m.top().stop()
		v := m.push(it.Resource)
		return m.startTop(v)
	case itemNamespace:
		return m.setNamespace(it.Name)
	case itemContext:
		if m.client != nil && it.Name == m.client.Context {
			return nil
		}
		m.connecting = it.Name
		name := it.Name
		return func() tea.Msg {
			c, err := k8s.New(name, "")
			return clientMsg{client: c, err: err}
		}
	}
	return nil
}

func (m *Model) startTop(v *resourceView) tea.Cmd {
	v.layout(m.width, m.bodyHeight(), "")
	return v.start(m.stream)
}

// pop removes the top view and resumes the one beneath it.
func (m *Model) pop() tea.Cmd {
	m.top().stop()
	m.stack = m.stack[:len(m.stack)-1]
	return m.startTop(m.top())
}

// setNamespace switches namespace, keeping the current resource and
// resetting the stack.
func (m *Model) setNamespace(ns string) tea.Cmd {
	if ns == m.namespace {
		return nil
	}
	m.namespace = ns
	res := m.top().res
	for _, v := range m.stack {
		v.stop()
	}
	m.stack = nil
	return m.startTop(m.push(res))
}

// useClient swaps in a new cluster connection and resets everything.
func (m *Model) useClient(c *k8s.Client) tea.Cmd {
	for _, v := range m.stack {
		v.stop()
	}
	m.client = c
	m.stream = clientStreamer{c}
	m.namespace = c.Namespace
	m.resources, m.namespaces = nil, nil
	m.err = nil
	res := k8s.Pods
	if len(m.stack) > 0 {
		res = m.top().res
	}
	m.stack = nil
	m.rebuildPalette()
	cmds := append([]tea.Cmd{m.startTop(m.push(res))}, m.discoverCmds()...)
	return tea.Batch(cmds...)
}

// View renders the palette (when open), the top view, and the status bar.
func (m Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	if m.width == 0 {
		return v
	}
	top := m.top()

	crumbs := make([]string, len(m.stack))
	for i, sv := range m.stack {
		crumbs[i] = sv.res.Name()
	}
	bar := ui.StatusBar{
		Namespace: m.namespace,
		Crumbs:    crumbs,
		Rows:      len(top.snapshot.Rows),
		State:     top.status.String(),
		Hint:      ": palette  esc back  q quit",
	}
	if m.client != nil {
		bar.Context = m.client.Context
	}
	switch {
	case m.connecting != "":
		bar.State = "connecting to " + m.connecting
	case m.err != nil:
		bar.Err = m.err.Error()
	case top.err != nil:
		bar.Err = top.err.Error()
	}

	parts := []string{}
	if m.palette.open {
		parts = append(parts, m.palette.view(m.width))
	}
	parts = append(parts, top.view(m.bodyHeight()), bar.Render(m.width))
	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, parts...))
	if m.client != nil {
		v.WindowTitle = "seaglass · " + m.client.Context
	}
	return v
}
