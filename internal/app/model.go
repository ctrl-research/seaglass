// Package app holds the root Bubble Tea model. Models are pure: all cluster
// I/O lives in internal/k8s and arrives here as messages.
package app

import (
	"context"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

// Options configures the root model.
type Options struct {
	Client    *k8s.Client
	Namespace string // "" means all namespaces
	Resource  schema.GroupVersionResource
}

// Model is the root application model.
type Model struct {
	opts    Options
	cancel  context.CancelFunc
	updates <-chan k8s.Update

	width, height int
	snapshot      k8s.Snapshot
	status        k8s.Status
	err           error
	colIdx        []int
	table         table.Model
}

// updateMsg carries one k8s.Update into the Bubble Tea loop.
type updateMsg k8s.Update

// New builds the root model. Streaming starts in Init.
func New(opts Options) Model {
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("81"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	t := table.New(table.WithFocused(true), table.WithStyles(styles))
	return Model{
		opts:   opts,
		status: k8s.StatusConnecting,
		table:  t,
	}
}

// Init starts the resource stream.
func (m Model) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.updates = m.opts.Client.Stream(ctx, m.opts.Resource, m.opts.Namespace)
	return tea.Batch(waitFor(m.updates), func() tea.Msg { return initMsg{cancel: cancel, updates: m.updates} })
}

// initMsg hands the stream handles back to the model, since Init operates on
// a copy.
type initMsg struct {
	cancel  context.CancelFunc
	updates <-chan k8s.Update
}

func waitFor(ch <-chan k8s.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return nil
		}
		return updateMsg(u)
	}
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case initMsg:
		m.cancel = msg.cancel
		m.updates = msg.updates
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout(m.selectedKey())
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd

	case updateMsg:
		// Capture identity before the snapshot changes underneath the cursor.
		selected := m.selectedKey()
		m.status = msg.Status
		m.err = msg.Err
		m.snapshot = msg.Snapshot
		m.layout(selected)
		return m, waitFor(m.updates)
	}
	return m, nil
}

// layout recomputes columns and rows for the current size and snapshot,
// restoring the cursor to the row identified by selectedKey when present.
func (m *Model) layout(selectedKey string) {
	if m.width == 0 {
		return
	}

	cols, idx := ui.FitColumns(m.snapshot.Columns, m.snapshot.Rows, m.width)
	m.colIdx = idx
	m.table.SetWidth(m.width)
	// One line for the header, one for the status bar.
	h := m.height - 2
	if h < 1 {
		h = 1
	}
	m.table.SetHeight(h)
	m.table.SetColumns(cols)
	m.table.SetRows(ui.ProjectRows(m.snapshot.Rows, idx))

	if selectedKey != "" {
		for i, r := range m.snapshot.Rows {
			if r.Key() == selectedKey {
				m.table.SetCursor(i)
				return
			}
		}
	}
	switch c := m.table.Cursor(); {
	case c < 0:
		m.table.SetCursor(0)
	case c >= len(m.snapshot.Rows):
		m.table.SetCursor(max(len(m.snapshot.Rows)-1, 0))
	}
}

func (m Model) selectedKey() string {
	c := m.table.Cursor()
	if c < 0 || c >= len(m.snapshot.Rows) {
		return ""
	}
	return m.snapshot.Rows[c].Key()
}

// View renders the table and status bar.
func (m Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	bar := ui.StatusBar{
		Context:   m.opts.Client.Context,
		Namespace: m.opts.Namespace,
		Resource:  m.opts.Resource.Resource,
		Rows:      len(m.snapshot.Rows),
		State:     m.status.String(),
	}
	if m.err != nil {
		bar.Err = m.err.Error()
	}

	var body string
	switch {
	case len(m.snapshot.Columns) == 0 && m.err != nil:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Padding(1, 2).Render(m.err.Error())
	case len(m.snapshot.Columns) == 0:
		body = lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("connecting to " + m.opts.Client.Context + "…")
	default:
		body = m.table.View()
	}
	body = lipgloss.NewStyle().Height(m.height - 1).MaxHeight(m.height - 1).Render(body)

	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, body, bar.Render(m.width)))
	v.AltScreen = true
	v.WindowTitle = "seaglass · " + m.opts.Client.Context
	return v
}
