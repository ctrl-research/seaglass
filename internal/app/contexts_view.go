package app

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// contextsView is a table of kubeconfig contexts. Enter switches to the
// selected one (after a confirm); it is opened from the palette's
// "clusters" entry so contexts are never mixed into resource filters.
type contextsView struct {
	infos   []k8s.ContextInfo
	current string
	table   table.Model
	placed  bool // initial cursor positioned after rows first appear
}

func newContextsView(infos []k8s.ContextInfo, current string) *contextsView {
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("81"))
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	t := table.New(table.WithFocused(true), table.WithStyles(styles))
	return &contextsView{infos: infos, current: current, table: t}
}

func (v *contextsView) start(deps) tea.Cmd { return nil }
func (v *contextsView) stop()              {}

func (v *contextsView) resize(width, height int) {
	cur := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	rows := make([]table.Row, len(v.infos))
	for i, ci := range v.infos {
		marker := "  "
		name := ci.Name
		if ci.Name == v.current {
			marker = cur.Render("● ")
			name = cur.Render(ci.Name)
		}
		rows[i] = table.Row{marker + name, ci.Cluster, ci.User, ci.Namespace}
	}
	// Fixed, evenly split columns.
	w := max((width-2)/4, 8)
	v.table.SetColumns([]table.Column{
		{Title: "CONTEXT", Width: w + (width - 2 - 4*w)},
		{Title: "CLUSTER", Width: w},
		{Title: "USER", Width: w},
		{Title: "NAMESPACE", Width: w},
	})
	v.table.SetWidth(width)
	v.table.SetHeight(max(height-1, 1))
	v.table.SetRows(rows)
	// Position the cursor on the current context the first time rows exist.
	if !v.placed {
		v.placed = true
		for i, ci := range v.infos {
			if ci.Name == v.current {
				v.table.SetCursor(i)
				break
			}
		}
	}
}

func (v *contextsView) selected() (k8s.ContextInfo, bool) {
	c := v.table.Cursor()
	if c < 0 || c >= len(v.infos) {
		return k8s.ContextInfo{}, false
	}
	return v.infos[c], true
}

func (v *contextsView) handleKey(msg tea.KeyPressMsg, _, _ int) (tea.Cmd, bool) {
	if is(msg, keys.Accept) || is(msg, keys.Back) {
		return nil, false // handled by the model (switch / pop)
	}
	var cmd tea.Cmd
	v.table, cmd = v.table.Update(msg)
	return cmd, true
}

func (v *contextsView) render(width, height int) string {
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(v.table.View())
}

func (v *contextsView) crumbs() []string { return []string{"clusters"} }

func (v *contextsView) capturesInput() bool { return false }

func (v *contextsView) status() viewStatus {
	return viewStatus{count: itoaInt64(int64(len(v.infos))) + " contexts"}
}

func (v *contextsView) hint() string { return "enter switch  ↑↓ move" }

func (v *contextsView) help() []helpSection {
	km := v.table.KeyMap
	return []helpSection{{"Clusters", []key.Binding{keys.Accept, km.LineUp, km.LineDown, keys.Back}}}
}
