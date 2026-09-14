package app

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// itemKind is what selecting a palette item does.
type itemKind int

const (
	itemResource itemKind = iota
	itemNamespace
	itemContext
)

func (k itemKind) String() string {
	switch k {
	case itemResource:
		return "resource"
	case itemNamespace:
		return "namespace"
	default:
		return "context"
	}
}

// paletteItem is one selectable entry.
type paletteItem struct {
	Kind     itemKind
	Label    string
	Detail   string
	Resource k8s.Resource // when Kind == itemResource
	Name     string       // namespace or context name
	search   string
}

// paletteMaxVisible caps the dropdown height.
const paletteMaxVisible = 10

// palette is the fuzzy command palette. It is a dropdown rendered at the top
// of the screen while open.
type palette struct {
	open    bool
	input   textinput.Model
	items   []paletteItem
	search  []string
	matches []int
	cursor  int
	width   int
}

var (
	palInputStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	palLabelStyle  = lipgloss.NewStyle()
	palDetailStyle = lipgloss.NewStyle().Faint(true)
	palKindStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	palSelStyle    = lipgloss.NewStyle().Background(lipgloss.Color("57")).Foreground(lipgloss.Color("229")).Bold(true)
	palEmptyStyle  = lipgloss.NewStyle().Faint(true).Italic(true)
)

func newPalette() palette {
	ti := textinput.New()
	ti.Prompt = " › "
	ti.Placeholder = "resource, namespace, or context"
	ti.SetVirtualCursor(true)
	return palette{input: ti}
}

// buildItems assembles palette entries from cluster state.
func buildItems(resources []k8s.Resource, namespaces, contexts []string) []paletteItem {
	items := make([]paletteItem, 0, len(resources)+len(namespaces)+len(contexts))
	for _, r := range resources {
		detail := r.GroupVersion()
		if len(r.ShortNames) > 0 {
			detail = strings.Join(r.ShortNames, ",") + " · " + detail
		}
		search := strings.ToLower(strings.Join(append([]string{r.Name(), r.Kind, r.GroupVersion()}, r.ShortNames...), " "))
		items = append(items, paletteItem{Kind: itemResource, Label: r.Name(), Detail: detail, Resource: r, search: search})
	}
	if len(namespaces) > 0 {
		items = append(items, paletteItem{Kind: itemNamespace, Label: "all namespaces", Detail: "namespace · every namespace at once", Name: "", search: "ns namespace all -a"})
	}
	for _, ns := range namespaces {
		items = append(items, paletteItem{Kind: itemNamespace, Label: ns, Detail: "namespace", Name: ns, search: strings.ToLower("ns namespace " + ns)})
	}
	for _, c := range contexts {
		items = append(items, paletteItem{Kind: itemContext, Label: c, Detail: "context", Name: c, search: strings.ToLower("ctx context " + c)})
	}
	return items
}

func (p *palette) setItems(items []paletteItem) {
	p.items = items
	p.search = make([]string, len(items))
	for i, it := range items {
		p.search[i] = it.search
	}
	p.filter()
}

func (p *palette) show() tea.Cmd {
	p.open = true
	p.input.Reset()
	p.cursor = 0
	p.filter()
	return p.input.Focus()
}

func (p *palette) hide() {
	p.open = false
	p.input.Blur()
}

// filter recomputes matches, ranked by score then original order.
func (p *palette) filter() {
	idx, scores := fuzzyFilter(p.input.Value(), p.search)
	p.matches = idx
	if scores != nil {
		sort.SliceStable(p.matches, func(i, j int) bool {
			return scores[p.matches[i]] > scores[p.matches[j]]
		})
	}
	p.clampCursor()
}

func (p *palette) clampCursor() {
	if p.cursor >= len(p.matches) {
		p.cursor = len(p.matches) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// selected returns the highlighted item, if any.
func (p *palette) selected() (paletteItem, bool) {
	if len(p.matches) == 0 {
		return paletteItem{}, false
	}
	return p.items[p.matches[p.cursor]], true
}

// update handles a key while open. It returns the chosen item when the user
// pressed enter, and closed=true when the palette should be dismissed.
func (p *palette) update(msg tea.KeyPressMsg) (chosen *paletteItem, closed bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		p.hide()
		return nil, true, nil
	case "enter":
		if it, ok := p.selected(); ok {
			p.hide()
			return &it, true, nil
		}
		return nil, false, nil
	case "up", "ctrl+p", "ctrl+k":
		p.cursor--
		p.clampCursor()
		return nil, false, nil
	case "down", "ctrl+n", "ctrl+j":
		p.cursor++
		p.clampCursor()
		return nil, false, nil
	}
	before := p.input.Value()
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.cursor = 0
		p.filter()
	}
	return nil, false, cmd
}

// height is the number of lines the dropdown occupies.
func (p *palette) height() int {
	if !p.open {
		return 0
	}
	n := min(len(p.matches), paletteMaxVisible)
	if n == 0 {
		n = 1
	}
	return 1 + n
}

// view renders the dropdown at the given width.
func (p *palette) view(width int) string {
	p.input.SetWidth(max(width-4, 10))
	lines := []string{palInputStyle.Render(p.input.View())}

	if len(p.matches) == 0 {
		lines = append(lines, palEmptyStyle.Render("   no matches"))
		return strings.Join(lines, "\n")
	}

	// Window the results around the cursor.
	start := 0
	if p.cursor >= paletteMaxVisible {
		start = p.cursor - paletteMaxVisible + 1
	}
	end := min(start+paletteMaxVisible, len(p.matches))

	labelW := 0
	for _, idx := range p.matches[start:end] {
		labelW = max(labelW, lipgloss.Width(p.items[idx].Label))
	}
	labelW = min(labelW, max(width/3, 12))

	for i := start; i < end; i++ {
		it := p.items[p.matches[i]]
		kind := it.Kind.String()
		label := lipgloss.NewStyle().Width(labelW).MaxWidth(labelW).Inline(true).Render(it.Label)
		detailW := width - 3 - labelW - 2 - len(kind) - 3
		detail := lipgloss.NewStyle().Width(max(detailW, 0)).MaxWidth(max(detailW, 0)).Inline(true).Render(it.Detail)
		row := fmt.Sprintf("   %s  %s %s ", label, detail, kind)
		if i == p.cursor {
			row = palSelStyle.Width(width).Render(row)
		} else {
			row = palLabelStyle.Render("   "+label) + "  " + palDetailStyle.Render(detail) + " " + palKindStyle.Render(kind) + " "
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}
