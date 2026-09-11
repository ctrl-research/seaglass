package app

import (
	"context"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

// streamer is the slice of k8s.Client the view needs; tests inject a fake.
type streamer interface {
	Stream(ctx context.Context, res k8s.Resource, namespace string) <-chan k8s.Update
}

// clientStreamer adapts k8s.Client to streamer.
type clientStreamer struct{ c *k8s.Client }

func (s clientStreamer) Stream(ctx context.Context, res k8s.Resource, ns string) <-chan k8s.Update {
	return s.c.Stream(ctx, res.GVR, ns)
}

// updateMsg carries one k8s.Update to the view with the matching id. Updates
// from a view that has since been stopped are dropped by id.
type updateMsg struct {
	id int
	k8s.Update
}

// resourceView is a live table of one resource in one namespace.
type resourceView struct {
	id        int
	res       k8s.Resource
	namespace string // "" means all namespaces (or cluster scope)

	cancel  context.CancelFunc
	updates <-chan k8s.Update

	snapshot k8s.Snapshot
	status   k8s.Status
	err      error
	colIdx   []int
	table    table.Model
}

func newResourceView(id int, res k8s.Resource, ns string) *resourceView {
	if !res.Namespaced {
		ns = ""
	}
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("81"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	return &resourceView{
		id:        id,
		res:       res,
		namespace: ns,
		status:    k8s.StatusConnecting,
		table:     table.New(table.WithFocused(true), table.WithStyles(styles)),
	}
}

// start begins streaming and returns the command that delivers the first
// update. Calling start on a running view restarts the stream.
func (v *resourceView) start(s streamer) tea.Cmd {
	v.stop()
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	v.updates = s.Stream(ctx, v.res, v.namespace)
	v.status = k8s.StatusConnecting
	return v.wait()
}

// stop cancels the stream. The view keeps its last snapshot for display.
func (v *resourceView) stop() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
}

func (v *resourceView) running() bool { return v.cancel != nil }

func (v *resourceView) wait() tea.Cmd {
	ch, id := v.updates, v.id
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return nil
		}
		return updateMsg{id: id, Update: u}
	}
}

// handle applies an update and re-arms the wait.
func (v *resourceView) handle(msg updateMsg, width, height int) tea.Cmd {
	selected := v.selectedKey()
	v.status = msg.Status
	v.err = msg.Err
	v.snapshot = msg.Snapshot
	v.layout(width, height, selected)
	if !v.running() {
		return nil
	}
	return v.wait()
}

// layout recomputes columns and rows for the given size, restoring the
// cursor to the row identified by selectedKey when present.
func (v *resourceView) layout(width, height int, selectedKey string) {
	if width <= 0 {
		return
	}
	cols, idx := ui.FitColumns(v.snapshot.Columns, v.snapshot.Rows, width)
	v.colIdx = idx
	v.table.SetWidth(width)
	v.table.SetHeight(max(height-1, 1)) // one line for the header
	v.table.SetColumns(cols)
	v.table.SetRows(ui.ProjectRows(v.snapshot.Rows, idx))

	if selectedKey != "" {
		for i, r := range v.snapshot.Rows {
			if r.Key() == selectedKey {
				v.table.SetCursor(i)
				return
			}
		}
	}
	switch c := v.table.Cursor(); {
	case c < 0:
		v.table.SetCursor(0)
	case c >= len(v.snapshot.Rows):
		v.table.SetCursor(max(len(v.snapshot.Rows)-1, 0))
	}
}

func (v *resourceView) selectedKey() string {
	c := v.table.Cursor()
	if c < 0 || c >= len(v.snapshot.Rows) {
		return ""
	}
	return v.snapshot.Rows[c].Key()
}

// selectedRow returns the highlighted row, if any.
func (v *resourceView) selectedRow() (k8s.Row, bool) {
	c := v.table.Cursor()
	if c < 0 || c >= len(v.snapshot.Rows) {
		return k8s.Row{}, false
	}
	return v.snapshot.Rows[c], true
}

func (v *resourceView) update(msg tea.KeyPressMsg) tea.Cmd {
	var cmd tea.Cmd
	v.table, cmd = v.table.Update(msg)
	return cmd
}

func (v *resourceView) view(height int) string {
	var body string
	switch {
	case len(v.snapshot.Columns) == 0 && v.err != nil:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Padding(1, 2).Render(v.err.Error())
	case len(v.snapshot.Columns) == 0:
		body = lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("loading " + v.res.Name() + "…")
	default:
		body = v.table.View()
	}
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
}
