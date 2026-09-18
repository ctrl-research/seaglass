package app

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/k8s"
	"github.com/ctrl-research/seaglass/internal/ui"
)

type objMode int

const (
	modeDetail objMode = iota
	modeYAML
)

func (m objMode) String() string {
	if m == modeYAML {
		return "yaml"
	}
	return "detail"
}

// objectMsg delivers a fetched object to the view with the matching id.
type objectMsg struct {
	id  int
	obj *unstructured.Unstructured
	err error
}

// objectView shows one object as a generic detail summary or as YAML.
type objectView struct {
	id        int
	res       k8s.Resource
	namespace string
	name      string
	mode      objMode

	obj     *unstructured.Unstructured
	yaml    string
	err     error
	loading bool

	vp            viewport.Model
	width, height int
}

func newObjectView(id int, res k8s.Resource, ns, name string, mode objMode) *objectView {
	if !res.Namespaced {
		ns = ""
	}
	return &objectView{id: id, res: res, namespace: ns, name: name, mode: mode, vp: viewport.New()}
}

func (v *objectView) start(d deps) tea.Cmd {
	v.loading = true
	id, res, ns, name, get := v.id, v.res, v.namespace, v.name, d.get
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		obj, err := get.Get(ctx, res, ns, name)
		return objectMsg{id: id, obj: obj, err: err}
	}
}

func (v *objectView) stop() {}

// handle applies a fetched object.
func (v *objectView) handle(msg objectMsg) {
	v.loading = false
	v.err = msg.err
	if msg.err != nil {
		return
	}
	v.obj = msg.obj
	v.yaml, v.err = k8s.ToYAML(msg.obj)
	v.refresh()
}

// refresh re-renders content for the current mode, keeping scroll position
// where possible.
func (v *objectView) refresh() {
	if v.obj == nil {
		return
	}
	off := v.vp.YOffset()
	switch v.mode {
	case modeYAML:
		v.vp.SetContent(ui.ColorizeYAML(v.yaml))
	default:
		content := ui.Describe(v.obj, time.Now())
		if k8s.IsFlux(v.res) {
			content = ui.FluxSummary(k8s.FluxDetail(v.obj)) + "\n\n" + content
		}
		v.vp.SetContent(content)
	}
	v.vp.SetYOffset(off)
}

func (v *objectView) resize(width, height int) {
	v.width, v.height = width, height
	v.vp.SetWidth(width)
	v.vp.SetHeight(max(height, 1))
}

func (v *objectView) handleKey(msg tea.KeyPressMsg, width, height int) (tea.Cmd, bool) {
	switch {
	case is(msg, keys.ModeYAML):
		v.mode = modeYAML
		v.vp.GotoTop()
		v.refresh()
		return nil, true
	case is(msg, keys.ModeDetail):
		v.mode = modeDetail
		v.vp.GotoTop()
		v.refresh()
		return nil, true
	case is(msg, keys.Copy):
		if v.yaml != "" {
			return tea.SetClipboard(v.yaml), true
		}
		return nil, true
	case is(msg, keys.Top):
		v.vp.GotoTop()
		return nil, true
	case is(msg, keys.Bottom):
		v.vp.GotoBottom()
		return nil, true
	case is(msg, keys.Back):
		return nil, false
	}
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return cmd, true
}

func (v *objectView) help() []helpSection {
	km := v.vp.KeyMap
	return []helpSection{
		{"Object", []key.Binding{keys.ModeYAML, keys.ModeDetail, keys.Edit, keys.Owner, keys.Related, keys.Copy, keys.Reload, keys.Top, keys.Bottom}},
		{"Scroll", []key.Binding{km.Up, km.Down, km.PageUp, km.PageDown, km.HalfPageUp, km.HalfPageDown}},
	}
}

func (v *objectView) render(width, height int) string {
	var body string
	switch {
	case v.err != nil:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Padding(1, 2).Render(v.err.Error())
	case v.loading && v.obj == nil:
		body = lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("loading " + v.res.Kind + " " + v.name + "…")
	default:
		body = v.vp.View()
	}
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
}

func (v *objectView) crumbs() []string { return []string{v.res.Name(), v.name} }

func (v *objectView) capturesInput() bool { return false }

func (v *objectView) status() viewStatus {
	state := v.mode.String()
	if v.vp.TotalLineCount() > v.vp.Height() {
		state = fmt.Sprintf("%s %d%%", state, int(v.vp.ScrollPercent()*100))
	}
	if v.loading {
		state = "loading"
	}
	return viewStatus{state: state, err: v.err}
}

func (v *objectView) hint() string {
	return "y yaml  d detail  e edit  o owner  J related  ? keys"
}
