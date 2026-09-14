package app

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

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
	status_  k8s.Status
	err      error
	colIdx   []int
	table    table.Model

	// Row filter. typing is true while the input has focus; the filter
	// stays applied after enter until esc clears it.
	filter   textinput.Model
	typing   bool
	filtered []k8s.Row // rows after sort and filter

	// Sort. sortCol is an index into snapshot.Columns, -1 for server order.
	sortCol  int
	sortDesc bool
	colMode  ui.ColumnMode
}

var (
	filterPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	filterCountStyle  = lipgloss.NewStyle().Faint(true)
)

func newResourceView(id int, res k8s.Resource, ns string) *resourceView {
	if !res.Namespaced {
		ns = ""
	}
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("81"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	fi := textinput.New()
	fi.Prompt = " / "
	fi.Placeholder = "filter rows"
	fi.SetVirtualCursor(true)
	return &resourceView{
		id:        id,
		res:       res,
		namespace: ns,
		status_:   k8s.StatusConnecting,
		table:     table.New(table.WithFocused(true), table.WithStyles(styles)),
		filter:    fi,
		sortCol:   -1,
	}
}

// setSort sorts by a snapshot column; choosing the current column flips
// the direction. -1 restores server order.
func (v *resourceView) setSort(col int) {
	if col == v.sortCol && col >= 0 {
		v.sortDesc = !v.sortDesc
	} else {
		v.sortCol, v.sortDesc = col, false
	}
}

// orderedRows returns snapshot rows in sort order (a copy when sorted).
func (v *resourceView) orderedRows() []k8s.Row {
	if v.sortCol < 0 || v.sortCol >= len(v.snapshot.Columns) {
		return v.snapshot.Rows
	}
	rows := make([]k8s.Row, len(v.snapshot.Rows))
	copy(rows, v.snapshot.Rows)
	ui.SortRows(rows, v.sortCol, v.sortDesc)
	return rows
}

// filterActive reports whether the filter line is shown.
func (v *resourceView) filterActive() bool { return v.typing || v.filter.Value() != "" }

// startFilter focuses the filter input.
func (v *resourceView) startFilter() tea.Cmd {
	v.typing = true
	return v.filter.Focus()
}

// clearFilter removes the filter and hides the line.
func (v *resourceView) clearFilter() {
	v.typing = false
	v.filter.Blur()
	v.filter.Reset()
}

// refilter re-applies sort and filter, keeping the selected row.
func (v *resourceView) refilter(width, height int) {
	key := v.selectedKey()
	v.applyFilter()
	v.layout(width, height, key)
}

// applyFilter recomputes filtered from the sorted snapshot rows.
// Rows match when every whitespace-separated term is a case-insensitive
// substring of the row's namespace or any cell. Fuzzy matching is wrong
// here: over a whole row, the letters of "core" appear in order in almost
// every pod.
func (v *resourceView) applyFilter() {
	rows := v.orderedRows()
	terms := strings.Fields(strings.ToLower(v.filter.Value()))
	if len(terms) == 0 {
		v.filtered = rows
		return
	}
	// Always a fresh slice: filtered may alias snapshot.Rows when unfiltered,
	// so reusing its backing array would overwrite the snapshot.
	out := make([]k8s.Row, 0, len(rows))
	for _, r := range rows {
		hay := strings.ToLower(r.Namespace + " " + strings.Join(r.Cells, " "))
		ok := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	v.filtered = out
}

// start begins streaming and returns the command that delivers the first
// update. Calling start on a running view restarts the stream.
func (v *resourceView) start(d deps) tea.Cmd {
	v.stop()
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	v.updates = d.stream.Stream(ctx, v.res, v.namespace)
	v.status_ = k8s.StatusConnecting
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
	v.status_ = msg.Status
	v.err = msg.Err
	v.snapshot = msg.Snapshot
	v.applyFilter()
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
	if v.filtered == nil {
		v.filtered = v.snapshot.Rows
	}
	// Decorate the sorted column's title before fitting so the arrow is
	// counted in its width.
	columns := v.snapshot.Columns
	if v.sortCol >= 0 && v.sortCol < len(columns) {
		columns = make([]k8s.Column, len(v.snapshot.Columns))
		copy(columns, v.snapshot.Columns)
		arrow := " ▲"
		if v.sortDesc {
			arrow = " ▼"
		}
		columns[v.sortCol].Name += arrow
	}
	// Widths come from every row so columns do not jump while typing.
	cols, idx := ui.FitColumns(columns, v.snapshot.Rows, width, v.colMode)
	v.colIdx = idx
	v.table.SetWidth(width)
	h := height - 1 // header
	if v.filterActive() {
		h-- // filter line
	}
	v.table.SetHeight(max(h, 1))
	// Clear rows before changing columns: the table re-renders existing
	// rows on SetColumns and panics when a new column has no cell.
	v.table.SetRows(nil)
	v.table.SetColumns(cols)
	v.table.SetRows(ui.ProjectRows(v.filtered, idx))

	if selectedKey != "" {
		for i, r := range v.filtered {
			if r.Key() == selectedKey {
				v.table.SetCursor(i)
				return
			}
		}
	}
	switch c := v.table.Cursor(); {
	case c < 0:
		v.table.SetCursor(0)
	case c >= len(v.filtered):
		v.table.SetCursor(max(len(v.filtered)-1, 0))
	}
}

func (v *resourceView) selectedKey() string {
	r, ok := v.selectedRow()
	if !ok {
		return ""
	}
	return r.Key()
}

// selectedRow returns the highlighted row, if any.
func (v *resourceView) selectedRow() (k8s.Row, bool) {
	c := v.table.Cursor()
	if c < 0 || c >= len(v.filtered) {
		return k8s.Row{}, false
	}
	return v.filtered[c], true
}

func (v *resourceView) resize(width, height int) { v.layout(width, height, v.selectedKey()) }

func (v *resourceView) crumbs() []string { return []string{v.res.Name()} }

func (v *resourceView) capturesInput() bool { return v.typing }

func (v *resourceView) status() viewStatus {
	count := fmt.Sprintf("%d rows", len(v.filtered))
	if len(v.snapshot.Rows) > len(v.filtered) {
		count = fmt.Sprintf("%d of %d rows", len(v.filtered), len(v.snapshot.Rows))
	}
	state := v.status_.String()
	if v.colMode != ui.ColumnsAuto {
		state = v.colMode.String() + " · " + state
	}
	return viewStatus{count: count, state: state, err: v.err}
}

func (v *resourceView) hint() string {
	return ": palette  / filter  s sort  w columns"
}

// handleKey handles a key. consumed is false when the key was not
// meaningful to the view and the caller may treat it as global.
func (v *resourceView) handleKey(msg tea.KeyPressMsg, width, height int) (cmd tea.Cmd, consumed bool) {
	if v.typing {
		switch msg.String() {
		case "esc":
			v.clearFilter()
			v.refilter(width, height)
			return nil, true
		case "enter":
			v.typing = false
			v.filter.Blur()
			if v.filter.Value() == "" {
				v.layout(width, height, v.selectedKey())
			}
			return nil, true
		case "up", "down", "pgup", "pgdown", "ctrl+n", "ctrl+p":
			v.table, cmd = v.table.Update(msg)
			return cmd, true
		}
		before := v.filter.Value()
		v.filter, cmd = v.filter.Update(msg)
		if v.filter.Value() != before {
			v.refilter(width, height)
		}
		return cmd, true
	}

	switch msg.String() {
	case "/":
		cmd = v.startFilter()
		v.layout(width, height, v.selectedKey())
		return cmd, true
	case "S":
		if v.sortCol >= 0 {
			v.sortDesc = !v.sortDesc
			v.refilter(width, height)
		}
		return nil, true
	case "w":
		v.colMode = v.colMode.Next()
		v.layout(width, height, v.selectedKey())
		return nil, true
	case "esc":
		if v.filter.Value() != "" {
			v.clearFilter()
			v.refilter(width, height)
			return nil, true
		}
		return nil, false
	}
	v.table, cmd = v.table.Update(msg)
	return cmd, true
}

func (v *resourceView) render(width, height int) string {
	var body string
	switch {
	case len(v.snapshot.Columns) == 0 && v.err != nil:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Padding(1, 2).Render(v.err.Error())
	case len(v.snapshot.Columns) == 0:
		body = lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("loading " + v.res.Name() + "…")
	default:
		body = v.table.View()
	}
	if v.filterActive() {
		body = lipgloss.JoinVertical(lipgloss.Left, v.filterLine(width), body)
	}
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
}

// filterLine renders the filter prompt with a match count.
func (v *resourceView) filterLine(width int) string {
	count := filterCountStyle.Render(fmt.Sprintf(" %d of %d ", len(v.filtered), len(v.snapshot.Rows)))
	hint := ""
	if v.typing {
		hint = filterCountStyle.Render("enter keep · esc clear ")
	} else {
		hint = filterCountStyle.Render("esc clear ")
	}
	right := count + hint
	v.filter.SetWidth(max(width-lipgloss.Width(right)-4, 10))
	left := filterPromptStyle.Render(v.filter.View())
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 0)
	return left + strings.Repeat(" ", gap) + right
}
