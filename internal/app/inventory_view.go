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

// inventoryRow is one fetched inventory object.
type inventoryRow struct {
	ref    k8s.ObjectRef
	res    k8s.Resource
	ready  string
	status string
	age    string
	err    error
}

// inventoryLoadedMsg delivers the fetched inventory rows.
type inventoryLoadedMsg struct {
	id   int
	rows []inventoryRow
}

// inventoryView lists the objects a Kustomization manages
// (status.inventory), fetched once. Enter opens an object's detail.
type inventoryView struct {
	id      int
	owner   string // "Kustomization/apps"
	refs    []k8s.ObjectRef
	deps    deps
	resolve func(kind, group string) (k8s.Resource, bool)

	rows          []inventoryRow
	loading       bool
	table         table.Model
	width, height int
}

func newInventoryView(id int, owner string, refs []k8s.ObjectRef, resolve func(kind, group string) (k8s.Resource, bool)) *inventoryView {
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("81"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	return &inventoryView{
		id: id, owner: owner, refs: refs, resolve: resolve, loading: true,
		table: table.New(table.WithFocused(true), table.WithStyles(styles)),
	}
}

func (v *inventoryView) start(d deps) tea.Cmd {
	v.deps = d
	id, refs, resolve, get := v.id, v.refs, v.resolve, d.get
	return func() tea.Msg {
		rows := make([]inventoryRow, len(refs))
		now := time.Now()
		for i, ref := range refs {
			row := inventoryRow{ref: ref}
			res, ok := resolve(ref.Kind, ref.Group)
			if !ok {
				row.err = fmt.Errorf("unknown kind %s", ref.Kind)
				rows[i] = row
				continue
			}
			row.res = res
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			obj, err := get.Get(ctx, res, ref.Namespace, ref.Name)
			cancel()
			if err != nil {
				row.err = err
			} else {
				row.ready, row.status = k8s.ReadyCondition(obj)
				row.age = k8s.AgeString(obj, now)
			}
			rows[i] = row
		}
		return inventoryLoadedMsg{id: id, rows: rows}
	}
}

func (v *inventoryView) stop() {}

func (v *inventoryView) handleLoaded(msg inventoryLoadedMsg) {
	v.loading = false
	v.rows = msg.rows
	sort.SliceStable(v.rows, func(a, b int) bool {
		ra, rb := notReady(v.rows[a].ready), notReady(v.rows[b].ready)
		if ra != rb {
			return ra
		}
		if v.rows[a].ref.Kind != v.rows[b].ref.Kind {
			return v.rows[a].ref.Kind < v.rows[b].ref.Kind
		}
		return v.rows[a].ref.Name < v.rows[b].ref.Name
	})
	v.layout()
	if v.table.Cursor() < 0 && len(v.rows) > 0 {
		v.table.SetCursor(0)
	}
}

func (v *inventoryView) layout() {
	if v.width == 0 {
		return
	}
	kindW, nsW, nameW := len("KIND"), len("NAMESPACE"), len("NAME")
	for _, r := range v.rows {
		kindW = max(kindW, len(r.ref.Kind))
		nsW = max(nsW, len(r.ref.Namespace))
		nameW = max(nameW, len(r.ref.Name))
	}
	statusW := max(v.width-kindW-nsW-nameW-6-5-12, 8)
	rows := make([]table.Row, len(v.rows))
	for i, r := range v.rows {
		status := r.status
		if r.err != nil {
			status = r.err.Error()
		}
		rows[i] = table.Row{
			r.ref.Kind, r.ref.Namespace, r.ref.Name,
			ui.StyleStatus(readyLabel(r.ready)), truncateStr(status, statusW), r.age,
		}
	}
	v.table.SetColumns([]table.Column{
		{Title: "KIND", Width: kindW}, {Title: "NAMESPACE", Width: nsW}, {Title: "NAME", Width: nameW},
		{Title: "READY", Width: 6}, {Title: "STATUS", Width: statusW}, {Title: "AGE", Width: 5},
	})
	v.table.SetWidth(v.width)
	v.table.SetHeight(max(v.height-1, 1))
	v.table.SetRows(rows)
}

func (v *inventoryView) resize(width, height int) {
	v.width, v.height = width, height
	v.layout()
}

func (v *inventoryView) selectedRow() (inventoryRow, bool) {
	c := v.table.Cursor()
	if c < 0 || c >= len(v.rows) {
		return inventoryRow{}, false
	}
	return v.rows[c], true
}

func (v *inventoryView) handleKey(msg tea.KeyPressMsg, _, _ int) (tea.Cmd, bool) {
	if is(msg, keys.Accept) || is(msg, keys.Detail) || is(msg, keys.YAML) || is(msg, keys.Back) {
		return nil, false
	}
	var cmd tea.Cmd
	v.table, cmd = v.table.Update(msg)
	return cmd, true
}

func (v *inventoryView) render(width, height int) string {
	if v.loading {
		return lipgloss.NewStyle().Height(height).Render(
			lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("loading inventory…"))
	}
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(v.table.View())
}

func (v *inventoryView) crumbs() []string { return []string{"inventory"} }

func (v *inventoryView) capturesInput() bool { return false }

func (v *inventoryView) status() viewStatus {
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
	return viewStatus{count: count}
}

func (v *inventoryView) hint() string { return "enter detail  y yaml" }

func (v *inventoryView) help() []helpSection {
	km := v.table.KeyMap
	return []helpSection{{"Inventory", []key.Binding{keys.Detail, keys.YAML, km.LineUp, km.LineDown, keys.Back}}}
}
