package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

// groupUpdateMsg carries one member's snapshot to the group view.
type groupUpdateMsg struct {
	id     int
	member int
	snap   k8s.Snapshot
}

// groupRow is a merged row with its source resource, for drill-down.
type groupRow struct {
	res       k8s.Resource
	namespace string
	name      string
	ready     string // Ready condition status, or ""
	status    string
	age       string
}

// groupView merges several resource streams into one table with a KIND
// column, not-ready rows first. Used by the "flux" and "workloads" groups.
type groupView struct {
	id      int
	title   string
	members []k8s.Resource

	cancel   context.CancelFunc
	events   <-chan groupUpdateMsg
	memberOf map[int][]groupRow // rows per member index
	loaded   int                // members that have reported at least once

	table         table.Model
	rows          []groupRow
	width, height int
	ns            string
}

func newGroupView(id int, title string, members []k8s.Resource, namespace string) *groupView {
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("81"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	return &groupView{
		id:       id,
		title:    title,
		members:  members,
		memberOf: map[int][]groupRow{},
		table:    table.New(table.WithFocused(true), table.WithStyles(styles)),
	}
}

func (v *groupView) start(d deps) tea.Cmd {
	namespace := v.ns
	v.stop()
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	merged := make(chan groupUpdateMsg, len(v.members)*2)
	v.events = merged
	for i, res := range v.members {
		ns := namespace
		if !res.Namespaced {
			ns = ""
		}
		ch := d.stream.Stream(ctx, res, ns, "", "", true)
		go func(idx int, ch <-chan k8s.Update) {
			for u := range ch {
				select {
				case merged <- groupUpdateMsg{id: v.id, member: idx, snap: u.Snapshot}:
				case <-ctx.Done():
					return
				}
			}
		}(i, ch)
	}
	return v.wait()
}

func (v *groupView) wait() tea.Cmd {
	ch := v.events
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return nil
		}
		return u
	}
}

func (v *groupView) stop() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
}

// handle applies a member's snapshot and re-arms the wait.
func (v *groupView) handle(msg groupUpdateMsg) tea.Cmd {
	if _, seen := v.memberOf[msg.member]; !seen {
		v.loaded++
	}
	res := v.members[msg.member]
	rows := make([]groupRow, 0, len(msg.snap.Rows))
	now := time.Now()
	for _, r := range msg.snap.Rows {
		gr := groupRow{res: res, namespace: r.Namespace, name: r.Name}
		if r.Object != nil {
			gr.ready, gr.status = k8s.ReadyCondition(r.Object)
			gr.age = k8s.AgeString(r.Object, now)
		}
		rows = append(rows, gr)
	}
	v.memberOf[msg.member] = rows
	v.rebuild()
	if v.cancel == nil {
		return nil
	}
	return v.wait()
}

// rebuild merges member rows, sorts not-ready first, and fills the table.
func (v *groupView) rebuild() {
	selected := v.selectedKey()
	v.rows = v.rows[:0]
	for i := range v.members {
		v.rows = append(v.rows, v.memberOf[i]...)
	}
	sort.SliceStable(v.rows, func(a, b int) bool {
		ra, rb := notReady(v.rows[a].ready), notReady(v.rows[b].ready)
		if ra != rb {
			return ra // not-ready first
		}
		if v.rows[a].res.Kind != v.rows[b].res.Kind {
			return v.rows[a].res.Kind < v.rows[b].res.Kind
		}
		if v.rows[a].namespace != v.rows[b].namespace {
			return v.rows[a].namespace < v.rows[b].namespace
		}
		return v.rows[a].name < v.rows[b].name
	})
	v.layout()
	if selected != "" {
		for i, r := range v.rows {
			if groupKey(r) == selected {
				v.table.SetCursor(i)
				return
			}
		}
	}
	// Position the cursor on the first row once there is one.
	if v.table.Cursor() < 0 && len(v.rows) > 0 {
		v.table.SetCursor(0)
	}
}

func notReady(status string) bool { return status != "" && status != "True" }

func groupKey(r groupRow) string { return r.res.Kind + "/" + r.namespace + "/" + r.name }

func (v *groupView) layout() {
	if v.width == 0 {
		return
	}
	// Fixed columns; widths from content.
	rows := make([]table.Row, len(v.rows))
	kindW, nsW, nameW := len("KIND"), len("NAMESPACE"), len("NAME")
	for _, r := range v.rows {
		kindW = max(kindW, len(r.res.Kind))
		nsW = max(nsW, len(r.namespace))
		nameW = max(nameW, len(r.name))
	}
	readyW := 6
	ageW := 5
	statusW := max(v.width-kindW-nsW-nameW-readyW-ageW-12, 10)
	for i, r := range v.rows {
		ready := r.ready
		if ready == "" {
			ready = "-"
		}
		rows[i] = table.Row{
			r.res.Kind, r.namespace, r.name,
			ui.StyleStatus(readyLabel(r.ready)),
			truncateStr(r.status, statusW), r.age,
		}
		_ = ready
	}
	v.table.SetColumns([]table.Column{
		{Title: "KIND", Width: kindW},
		{Title: "NAMESPACE", Width: nsW},
		{Title: "NAME", Width: nameW},
		{Title: "READY", Width: readyW},
		{Title: "STATUS", Width: statusW},
		{Title: "AGE", Width: ageW},
	})
	v.table.SetWidth(v.width)
	v.table.SetHeight(max(v.height-1, 1))
	v.table.SetRows(rows)
}

// readyLabel maps a Ready status to a colorable token.
func readyLabel(status string) string {
	switch status {
	case "True":
		return "Running" // green via StyleStatus
	case "False":
		return "Error" // red
	case "":
		return "-"
	default:
		return status
	}
}

func truncateStr(s string, n int) string {
	if n < 1 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n < 2 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func (v *groupView) resize(width, height int) {
	v.width, v.height = width, height
	v.layout()
}

func (v *groupView) selectedKey() string {
	r, ok := v.selectedRow()
	if !ok {
		return ""
	}
	return groupKey(r)
}

func (v *groupView) selectedRow() (groupRow, bool) {
	c := v.table.Cursor()
	if c < 0 || c >= len(v.rows) {
		return groupRow{}, false
	}
	return v.rows[c], true
}

func (v *groupView) handleKey(msg tea.KeyPressMsg, _, _ int) (tea.Cmd, bool) {
	if is(msg, keys.Accept) || is(msg, keys.Detail) || is(msg, keys.YAML) || is(msg, keys.Back) {
		return nil, false // handled by the model (open detail / pop)
	}
	var cmd tea.Cmd
	v.table, cmd = v.table.Update(msg)
	return cmd, true
}

func (v *groupView) render(width, height int) string {
	if v.loaded < len(v.members) && len(v.rows) == 0 {
		return lipgloss.NewStyle().Height(height).Render(
			lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("loading " + v.title + "…"))
	}
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(v.table.View())
}

func (v *groupView) crumbs() []string { return []string{v.title} }

func (v *groupView) capturesInput() bool { return false }

func (v *groupView) status() viewStatus {
	notReadyN := 0
	for _, r := range v.rows {
		if notReady(r.ready) {
			notReadyN++
		}
	}
	count := fmt.Sprintf("%d objects", len(v.rows))
	if notReadyN > 0 {
		count = fmt.Sprintf("%d not ready · %d objects", notReadyN, len(v.rows))
	}
	return viewStatus{count: count, state: "live"}
}

func (v *groupView) hint() string { return "enter detail  y yaml" }

func (v *groupView) help() []helpSection {
	km := v.table.KeyMap
	return []helpSection{{"Group", []key.Binding{keys.Detail, keys.YAML, km.LineUp, km.LineDown, keys.Back}}}
}

// setNamespace sets the namespace scope, called before start.
func (v *groupView) setNamespace(ns string) { v.ns = ns }
