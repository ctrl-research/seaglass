package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
	itemSort
	itemAction
	itemContainer
	itemSince
	itemExec
	itemPort
	itemCopy
	itemRelated
)

func (k itemKind) String() string {
	switch k {
	case itemResource:
		return "resource"
	case itemNamespace:
		return "namespace"
	case itemSort:
		return "column"
	case itemAction:
		return "action"
	case itemContainer:
		return "container"
	case itemSince:
		return "since"
	case itemExec:
		return "shell"
	case itemPort:
		return "port"
	case itemCopy:
		return "copy"
	case itemRelated:
		return "jump"
	default:
		return "context"
	}
}

// Palette actions.
const (
	actionQuit     = "quit"
	actionHelp     = "help"
	actionForwards = "forwards"
	actionEvents   = "events"
	actionContexts = "clusters"
)

// paletteItem is one selectable entry.
type paletteItem struct {
	Kind     itemKind
	Label    string
	Detail   string
	Resource k8s.Resource // when Kind == itemResource
	Name     string       // namespace or context name
	Index    int          // column index when Kind == itemSort
	Since    time.Duration
	Text     string // clipboard text when Kind == itemCopy
	search   string
	exact    []string // lowercased aliases that, matched exactly, rank first
}

// paletteMaxVisible caps the dropdown height.
const paletteMaxVisible = 10

// palette is the fuzzy command palette. It is a dropdown rendered at the top
// of the screen while open.
type palette struct {
	open    bool
	input   textinput.Model
	base    []paletteItem // the global item set
	items   []paletteItem // what is currently shown
	search  []string
	matches []int
	cursor  int
	width   int
	// hasContexts is true when the current item set contains contexts, which
	// are gated behind a "ctx"/"cluster" prefix so a filter never switches
	// clusters by accident.
	hasContexts bool
	// showActions mirrors the model flag, for the input-line hint only.
	showActions bool
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

const palettePlaceholder = "resource, namespace, or context"

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
		exact := append([]string{strings.ToLower(r.Name()), strings.ToLower(r.Kind)}, lowerAll(r.ShortNames)...)
		items = append(items, paletteItem{Kind: itemResource, Label: r.Name(), Detail: detail, Resource: r, search: search, exact: exact})
	}
	if len(namespaces) > 0 {
		items = append(items, paletteItem{Kind: itemNamespace, Label: "all namespaces", Detail: "namespace · every namespace at once", Name: "", search: "ns namespace all -a"})
	}
	for _, ns := range namespaces {
		items = append(items, paletteItem{Kind: itemNamespace, Label: ns, Detail: "namespace", Name: ns, search: strings.ToLower("ns namespace " + ns)})
	}
	for _, c := range contexts {
		items = append(items, paletteItem{Kind: itemContext, Label: c, Detail: "context · switch cluster", Name: c, search: strings.ToLower("ctx cluster context " + c)})
	}
	items = append(items,
		paletteItem{Kind: itemAction, Label: "clusters", Detail: "switch context · a table of all clusters", Name: actionContexts, search: "clusters cluster ctx context switch"},
		paletteItem{Kind: itemAction, Label: "events", Detail: "cluster events, newest first, warnings highlighted", Name: actionEvents, search: "events warnings ev"},
		paletteItem{Kind: itemAction, Label: "port-forwards", Detail: "list and cancel active port-forwards", Name: actionForwards, search: "port forwards proxy tunnel"},
		paletteItem{Kind: itemAction, Label: "help", Detail: "show every key for this view", Name: actionHelp, search: "help keys ?"},
		paletteItem{Kind: itemAction, Label: "quit", Detail: "exit seaglass", Name: actionQuit, search: "quit exit q :q"},
	)
	return items
}

// portItems builds the remote-port picker for a forward.
func portItems(ports []uint16) []paletteItem {
	items := make([]paletteItem, len(ports))
	for i, p := range ports {
		items[i] = paletteItem{Kind: itemPort, Label: strconv.Itoa(int(p)), Detail: "forward this container port", Index: int(p), search: strconv.Itoa(int(p)) + " port"}
	}
	return items
}

// setItems replaces the global item set.
func (p *palette) setItems(items []paletteItem) {
	p.base = items
	if !p.open {
		p.use(items)
	}
}

func (p *palette) use(items []paletteItem) {
	p.items = items
	p.search = make([]string, len(items))
	p.hasContexts = false
	for i, it := range items {
		p.search[i] = it.search
		if it.Kind == itemContext {
			p.hasContexts = true
		}
	}
	p.filter()
}

// show opens the palette over the global items.
func (p *palette) show() tea.Cmd {
	return p.showWith(p.base, palettePlaceholder)
}

// showExtra opens the palette with contextual items (e.g. actions for the
// selected row) listed before the global ones.
func (p *palette) showExtra(extra []paletteItem) tea.Cmd {
	if len(extra) == 0 {
		return p.show()
	}
	items := make([]paletteItem, 0, len(extra)+len(p.base))
	items = append(items, extra...)
	items = append(items, p.base...)
	return p.showWith(items, palettePlaceholder)
}

// showWith opens the palette over a temporary item set, e.g. sort columns.
// hide restores the global set.
func (p *palette) showWith(items []paletteItem, placeholder string) tea.Cmd {
	p.open = true
	p.input.Reset()
	p.input.Placeholder = placeholder
	p.cursor = 0
	p.use(items)
	return p.input.Focus()
}

func (p *palette) hide() {
	p.open = false
	p.input.Blur()
	p.input.Placeholder = palettePlaceholder
	p.use(p.base)
}

// sortItems builds the sort picker for a set of columns.
func sortItems(cols []k8s.Column, current int) []paletteItem {
	items := make([]paletteItem, 0, len(cols)+1)
	for i, c := range cols {
		detail := "sort by column"
		if i == current {
			detail = "current · choose again to reverse"
		}
		items = append(items, paletteItem{Kind: itemSort, Label: c.Name, Detail: detail, Index: i, search: strings.ToLower(c.Name)})
	}
	items = append(items, paletteItem{Kind: itemSort, Label: "server order", Detail: "namespace, then name", Index: -1, search: "server order default none"})
	return items
}

// filter recomputes matches, ranked by score then original order. Contexts
// only appear when the query begins with "ctx" or "cluster"; otherwise they
// are hidden so a resource/namespace filter can never select one.
func (p *palette) filter() {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	contextMode := false
	if p.hasContexts {
		// Only enter inline context mode when a name follows the keyword;
		// a bare "ctx" leaves the "clusters" entry (which opens the table).
		if rest, ok := stripKeyword(q, "ctx", "cluster", "context"); ok && rest != "" {
			contextMode = true
			q = rest
		}
	}
	idx, scores := fuzzyFilter(q, p.search)
	// Boost an exact short-name/name match so e.g. "ks" ranks kustomizations
	// first over incidental fuzzy matches.
	if q != "" && scores != nil {
		for _, i := range idx {
			for _, a := range p.items[i].exact {
				if a == q {
					scores[i] += 100000
					break
				}
			}
		}
	}
	if p.hasContexts {
		filtered := idx[:0]
		for _, i := range idx {
			isCtx := p.items[i].Kind == itemContext
			if contextMode != isCtx {
				continue
			}
			filtered = append(filtered, i)
		}
		idx = filtered
	}
	p.matches = idx
	if scores != nil {
		sort.SliceStable(p.matches, func(i, j int) bool {
			return scores[p.matches[i]] > scores[p.matches[j]]
		})
	}
	p.clampCursor()
}

// stripKeyword returns the remainder of q after a leading keyword (as a
// whole first word), and whether one was present. "ctx", "ctx ", and
// "ctx prod" all match; "context-name" does not.
func stripKeyword(q string, keywords ...string) (string, bool) {
	for _, kw := range keywords {
		if q == kw {
			return "", true
		}
		if strings.HasPrefix(q, kw+" ") {
			return strings.TrimSpace(q[len(kw):]), true
		}
	}
	return q, false
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
	switch {
	case is(msg, keys.Back):
		p.hide()
		return nil, true, nil
	case is(msg, keys.Accept):
		if it, ok := p.selected(); ok {
			p.hide()
			return &it, true, nil
		}
		return nil, false, nil
	case is(msg, keys.Up):
		p.cursor--
		p.clampCursor()
		return nil, false, nil
	case is(msg, keys.Down):
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
	hint := palKindStyle.Render("ctrl+a actions " + onOff(p.showActions))
	p.input.SetWidth(max(width-4-lipgloss.Width(hint)-2, 10))
	inputLine := palInputStyle.Render(p.input.View())
	gap := max(width-lipgloss.Width(inputLine)-lipgloss.Width(hint)-1, 1)
	lines := []string{inputLine + strings.Repeat(" ", gap) + hint}

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

// containerItems builds the container picker for a logs view.
func containerItems(containers []string, selected string) []paletteItem {
	items := []paletteItem{{Kind: itemContainer, Label: "all containers", Detail: "merged, ordered by time", Name: "", search: "all containers merged"}}
	for _, c := range containers {
		detail := "container"
		if c == selected {
			detail = "current"
		}
		items = append(items, paletteItem{Kind: itemContainer, Label: c, Detail: detail, Name: c, search: strings.ToLower(c + " container")})
	}
	return items
}

// sinceItems builds the time window picker for a logs view.
func sinceItems(current time.Duration) []paletteItem {
	items := make([]paletteItem, 0, len(sinceChoices))
	for _, c := range sinceChoices {
		detail := "show lines from the last " + c.label
		if c.d == 0 {
			detail = "no time limit (last " + fmt.Sprint(defaultTail) + " lines per container)"
		}
		if c.d == current {
			detail += " · current"
		}
		items = append(items, paletteItem{Kind: itemSince, Label: c.label, Detail: detail, Since: c.d, search: strings.ToLower(c.label + " since " + detail)})
	}
	return items
}

// execItems builds the container picker for a shell.
func execItems(containers []string) []paletteItem {
	items := make([]paletteItem, 0, len(containers))
	for _, c := range containers {
		items = append(items, paletteItem{Kind: itemExec, Label: c, Detail: "open a shell in this container", Name: c, search: strings.ToLower(c + " shell exec")})
	}
	return items
}

// copyItems builds the copy picker for a selected object.
func copyItems(res k8s.Resource, namespace, name, context string) []paletteItem {
	kubectl := "kubectl"
	if context != "" {
		kubectl += " --context " + context
	}
	nsFlag := ""
	if namespace != "" {
		nsFlag = " -n " + namespace
	}
	ref := res.Name() + "/" + name
	full := name
	if namespace != "" {
		full = namespace + "/" + name
	}
	entries := []struct{ label, detail, text string }{
		{"name", name, name},
		{"namespace/name", full, full},
		{"kubectl get", kubectl + nsFlag + " get " + ref, kubectl + nsFlag + " get " + ref},
		{"kubectl describe", kubectl + nsFlag + " describe " + ref, kubectl + nsFlag + " describe " + ref},
		{"kubectl get -o yaml", "…get " + ref + " -o yaml", kubectl + nsFlag + " get " + ref + " -o yaml"},
	}
	if namespace != "" {
		entries = append(entries[:1], append([]struct{ label, detail, text string }{{"namespace", namespace, namespace}}, entries[1:]...)...)
	}
	items := make([]paletteItem, len(entries))
	for i, e := range entries {
		items[i] = paletteItem{Kind: itemCopy, Label: e.label, Detail: e.detail, Text: e.text, search: strings.ToLower(e.label + " " + e.text)}
	}
	return items
}

// relatedItems builds the picker for related jumps.
func relatedItems(targets []relatedTarget) []paletteItem {
	items := make([]paletteItem, len(targets))
	for i, t := range targets {
		items[i] = paletteItem{Kind: itemRelated, Label: t.label, Detail: t.detail, Index: i, search: strings.ToLower(t.label + " " + t.detail)}
	}
	return items
}

func lowerAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(s)
	}
	return out
}
