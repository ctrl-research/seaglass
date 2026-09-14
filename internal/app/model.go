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

	"github.com/ctrl-research/seaglass/internal/config"
	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

// stateStore persists UI state between runs. Nil disables persistence.
type stateStore interface {
	Load() (config.State, error)
	Save(config.State) error
}

// Options configures the root model.
type Options struct {
	Client    *k8s.Client
	Namespace string // "" means all namespaces
	Resource  k8s.Resource
	Version   string // seaglass build version for the header
	// State persists the last context, namespace, and resource. Optional.
	State stateStore

	// streamer and getter override the client for tests.
	streamer streamer
	getter   getter
	// contexts overrides kubeconfig context discovery for tests.
	contexts []string
}

// Model is the root application model: a navigation stack of resource
// views, a command palette, and the cluster connection.
type Model struct {
	client    *k8s.Client
	deps      deps
	namespace string

	stack  []view
	nextID int

	palette    palette
	resources  []k8s.Resource
	namespaces []string
	contexts   []string

	connecting string // context name while switching, "" otherwise
	err        error
	showHelp   bool

	version       string // seaglass version
	serverVersion string // fetched from /version

	state     stateStore
	lastSaved string // fingerprint of the last persisted position

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
	versionMsg struct {
		version string
		err     error
	}
)

// New builds the root model. Streaming starts in Init.
func New(opts Options) Model {
	m := Model{
		client:    opts.Client,
		deps:      deps{stream: opts.streamer, get: opts.getter},
		namespace: opts.Namespace,
		palette:   newPalette(),
		contexts:  opts.contexts,
		version:   opts.Version,
		state:     opts.State,
	}
	if m.deps.stream == nil && opts.Client != nil {
		m.deps.stream = clientStreamer{opts.Client}
	}
	if m.deps.get == nil && opts.Client != nil {
		m.deps.get = opts.Client
	}
	m.pushResource(opts.Resource)
	return m
}

// Init starts the first stream and kicks off discovery.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.top().start(m.deps)}
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
			v, err := c.ServerVersion(context.Background())
			return versionMsg{version: v, err: err}
		},
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			names, err := c.NamespaceNames(ctx)
			return namespacesMsg{names: names, err: err}
		},
	}
}

func (m *Model) top() view { return m.stack[len(m.stack)-1] }

// pushResource adds a table view for res on top of the stack. It does not
// start it.
func (m *Model) pushResource(res k8s.Resource) *resourceView {
	m.nextID++
	v := newResourceView(m.nextID, res, m.namespace)
	m.stack = append(m.stack, v)
	return v
}

// pushObject adds an object view on top of the stack. It does not start it.
func (m *Model) pushObject(res k8s.Resource, ns, name string, mode objMode) *objectView {
	m.nextID++
	v := newObjectView(m.nextID, res, ns, name, mode)
	m.stack = append(m.stack, v)
	return v
}

// currentResource is the resource of the nearest table view, or Pods.
func (m *Model) currentResource() k8s.Resource {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if rv, ok := m.stack[i].(*resourceView); ok {
			return rv.res
		}
	}
	return k8s.Pods
}

// header describes the top block for the current state.
func (m Model) header() ui.Header {
	ns := m.namespace
	if ns == "" {
		ns = "all"
	}
	h := ui.Header{
		Left:  []ui.Field{{Key: "context", Value: "-"}, {Key: "namespace", Value: ns}, {Key: "user", Value: "-"}},
		Right: []ui.Field{{Key: "cluster", Value: "-"}, {Key: "k8s", Value: orDash(m.serverVersion)}, {Key: "seaglass", Value: orDash(m.version)}},
	}
	if m.client != nil {
		h.Left[0].Value = m.client.Context
		h.Left[2].Value = orDash(m.client.User)
		h.Right[0].Value = orDash(m.client.Host)
	}
	return h
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// headerHeight is the lines the header takes, 0 when hidden.
func (m Model) headerHeight() int {
	if m.header().Visible(m.width, m.height) {
		return ui.HeaderHeight
	}
	return 0
}

// bodyHeight is the height available to the top view.
func (m Model) bodyHeight() int {
	return max(m.height-1-m.headerHeight()-m.palette.height(), 1)
}

func (m *Model) rebuildPalette() {
	m.palette.setItems(buildItems(m.resources, m.namespaces, m.contexts))
}

// Update handles messages, then persists the position if it changed.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm := next.(Model)
	if save := nm.persist(); save != nil {
		cmd = tea.Batch(cmd, save)
	}
	return nm, cmd
}

// persist returns a command that saves the current position when it
// differs from the last save.
func (m *Model) persist() tea.Cmd {
	if m.state == nil || m.client == nil || m.connecting != "" {
		return nil
	}
	res := m.currentResource()
	key := m.client.Context + "\x00" + m.namespace + "\x00" + res.GVR.String()
	if key == m.lastSaved {
		return nil
	}
	m.lastSaved = key
	store, ctx, ns := m.state, m.client.Context, m.namespace
	return func() tea.Msg {
		st, err := store.Load()
		if err != nil {
			slog.Warn("load state", "err", err)
			st = config.State{}
		}
		st.Update(ctx, config.ContextState{Namespace: ns, AllNamespaces: ns == "", Resource: &res})
		if err := store.Save(st); err != nil {
			slog.Warn("save state", "err", err)
		}
		return nil
	}
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.palette.width = msg.Width
		m.top().resize(m.width, m.bodyHeight())
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case updateMsg:
		// Only the top view is live; anything else was popped or restarted.
		if rv, ok := m.top().(*resourceView); ok && rv.id == msg.id {
			return m, rv.handle(msg, m.width, m.bodyHeight())
		}
		return m, nil

	case objectMsg:
		if ov, ok := m.top().(*objectView); ok && ov.id == msg.id {
			ov.handle(msg)
		}
		return m, nil

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

	case versionMsg:
		if msg.err == nil {
			m.serverVersion = msg.version
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	slog.Debug("key", "key", msg.String(), "palette", m.palette.open, "help", m.showHelp, "stack", len(m.stack))
	if m.showHelp {
		if is(msg, keys.Help) || is(msg, keys.Back) || is(msg, keys.Quit) {
			m.showHelp = false
		}
		return m, nil
	}
	if m.palette.open {
		chosen, closed, cmd := m.palette.update(msg)
		if closed {
			m.top().resize(m.width, m.bodyHeight())
		}
		if chosen != nil {
			return m, tea.Batch(cmd, m.choose(*chosen))
		}
		return m, cmd
	}

	top := m.top()
	if top.capturesInput() {
		cmd, _ := top.handleKey(msg, m.width, m.bodyHeight())
		return m, cmd
	}

	switch {
	case is(msg, keys.Quit):
		for _, v := range m.stack {
			v.stop()
		}
		return m, tea.Quit
	case is(msg, keys.Palette):
		cmd := m.palette.show()
		top.resize(m.width, m.bodyHeight())
		return m, cmd
	case is(msg, keys.Help):
		m.showHelp = true
		return m, nil
	}

	// Drill into the selected row of a table view.
	if rv, ok := top.(*resourceView); ok {
		switch {
		case is(msg, keys.Sort):
			if len(rv.snapshot.Columns) == 0 {
				return m, nil
			}
			cmd := m.palette.showWith(sortItems(rv.snapshot.Columns, rv.sortCol), "sort by column")
			rv.resize(m.width, m.bodyHeight())
			return m, cmd
		case is(msg, keys.Detail), is(msg, keys.YAML):
			row, ok := rv.selectedRow()
			if !ok {
				return m, nil
			}
			mode := modeDetail
			if is(msg, keys.YAML) {
				mode = modeYAML
			}
			ns := row.Namespace
			if ns == "" {
				ns = rv.namespace
			}
			rv.stop()
			m.pushObject(rv.res, ns, row.Name, mode)
			return m, m.startTop()
		}
	}
	if ov, ok := top.(*objectView); ok && is(msg, keys.Reload) {
		return m, ov.start(m.deps)
	}

	// The view gets first refusal (a table uses esc to clear its filter).
	cmd, consumed := top.handleKey(msg, m.width, m.bodyHeight())
	if consumed {
		return m, cmd
	}
	if is(msg, keys.Back) {
		if len(m.stack) > 1 {
			return m, m.pop()
		}
		m.err = nil
	}
	return m, nil
}

// helpSections assembles the overlay: global, then the top view's, then
// the palette's.
func (m Model) helpSections() []ui.HelpSection {
	secs := append([]helpSection{globalHelp()}, m.top().help()...)
	secs = append(secs, paletteHelp())
	out := make([]ui.HelpSection, len(secs))
	for i, s := range secs {
		out[i] = ui.HelpSection{Title: s.title, Bindings: s.bindings}
	}
	return out
}

// choose acts on a palette selection.
func (m *Model) choose(it paletteItem) tea.Cmd {
	switch it.Kind {
	case itemSort:
		if rv, ok := m.top().(*resourceView); ok {
			rv.setSort(it.Index)
			rv.refilter(m.width, m.bodyHeight())
		}
		return nil
	case itemResource:
		if _, isTable := m.top().(*resourceView); isTable && it.Resource.GVR == m.currentResource().GVR {
			return nil
		}
		m.top().stop()
		m.pushResource(it.Resource)
		return m.startTop()
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

// startTop sizes and starts the top view.
func (m *Model) startTop() tea.Cmd {
	v := m.top()
	v.resize(m.width, m.bodyHeight())
	return v.start(m.deps)
}

// pop removes the top view and resumes the one beneath it.
func (m *Model) pop() tea.Cmd {
	m.top().stop()
	m.stack = m.stack[:len(m.stack)-1]
	return m.startTop()
}

// setNamespace switches namespace, keeping the current resource and
// resetting the stack.
func (m *Model) setNamespace(ns string) tea.Cmd {
	if ns == m.namespace {
		return nil
	}
	m.namespace = ns
	res := m.currentResource()
	for _, v := range m.stack {
		v.stop()
	}
	m.stack = nil
	m.pushResource(res)
	return m.startTop()
}

// useClient swaps in a new cluster connection and resets everything.
func (m *Model) useClient(c *k8s.Client) tea.Cmd {
	for _, v := range m.stack {
		v.stop()
	}
	m.client = c
	m.deps = deps{stream: clientStreamer{c}, get: c}
	m.namespace = c.Namespace
	m.resources, m.namespaces = nil, nil
	m.serverVersion = ""
	m.err = nil
	res := m.currentResource()
	m.stack = nil
	m.rebuildPalette()
	m.pushResource(res)
	cmds := append([]tea.Cmd{m.startTop()}, m.discoverCmds()...)
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

	// Breadcrumbs describe scope, large to small: context › namespace ›
	// resource › object. The view stack is history, not scope, so it is not
	// shown; the hint names where esc goes instead.
	// Keep the hint short; the bar drops it when it does not fit, keeping
	// the back target longer.
	back := ""
	if len(m.stack) > 1 {
		c := m.stack[len(m.stack)-2].crumbs()
		back = "esc back to " + c[len(c)-1]
	}
	st := top.status()
	bar := ui.StatusBar{
		Namespace: m.namespace,
		Crumbs:    top.crumbs(),
		Count:     st.count,
		State:     st.state,
		Hint:      top.hint(),
		Back:      back,
	}
	if m.client != nil {
		bar.Context = m.client.Context
	}
	switch {
	case m.connecting != "":
		bar.State = "connecting to " + m.connecting
	case m.err != nil:
		bar.Err = m.err.Error()
	case st.err != nil:
		bar.Err = st.err.Error()
	}

	parts := []string{}
	if m.headerHeight() > 0 {
		parts = append(parts, m.header().Render(m.width))
	}
	if m.palette.open {
		parts = append(parts, m.palette.view(m.width))
	}
	if m.showHelp {
		bar.Hint, bar.Back = "? or esc closes help", ""
		parts = append(parts, ui.RenderHelp(m.helpSections(), m.width, m.bodyHeight()))
	} else {
		parts = append(parts, top.render(m.width, m.bodyHeight()))
	}
	parts = append(parts, bar.Render(m.width))
	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, parts...))
	if m.client != nil {
		v.WindowTitle = "seaglass · " + m.client.Context
	}
	return v
}
