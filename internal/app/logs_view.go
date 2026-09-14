package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// logger is the slice of k8s.Client the logs view needs.
type logger interface {
	Logs(ctx context.Context, namespace, pod, container string, opts k8s.LogOptions) (<-chan k8s.LogEvent, error)
}

// maxLogLines caps memory per view; older lines are dropped.
const maxLogLines = 10000

// defaultTail is how many lines each container backfills on open.
const defaultTail = 1000

type (
	// logLinesMsg delivers a batch of events to the view with matching id.
	logLinesMsg struct {
		id     int
		events []k8s.LogEvent
	}
	// containersMsg delivers the pod's container names.
	containersMsg struct {
		id    int
		names []string
		err   error
	}
)

var containerColors = []string{"81", "114", "214", "176", "203", "79", "141", "223"}

var (
	logTSStyle    = lipgloss.NewStyle().Faint(true)
	logMatchStyle = lipgloss.NewStyle().Background(lipgloss.Color("214")).Foreground(lipgloss.Color("16"))
	logEndStyle   = lipgloss.NewStyle().Faint(true).Italic(true)
)

// logsView streams and shows container logs for one pod.
type logsView struct {
	id        int
	namespace string
	pod       string
	deps      deps

	containers []string // all containers; nil until fetched
	selected   string   // "" means all containers
	opts       k8s.LogOptions

	cancel   context.CancelFunc
	events   <-chan k8s.LogEvent
	streams  int // running streams; ended counts down
	lines    []k8s.LogLine
	err      error
	loading  bool
	endedMsg string

	showTS bool
	wrap   bool
	follow bool

	filter textinput.Model
	typing bool
	re     *regexp.Regexp
	reErr  string
	shown  int // lines currently rendered (after filter)

	vp            viewport.Model
	width, height int
	saveDir       string
}

func newLogsView(id int, ns, pod, saveDir string) *logsView {
	fi := textinput.New()
	fi.Prompt = " / "
	fi.Placeholder = "regex filter"
	fi.SetVirtualCursor(true)
	vp := viewport.New()
	return &logsView{
		id:        id,
		namespace: ns,
		pod:       pod,
		opts:      k8s.LogOptions{Follow: true, TailLines: defaultTail},
		follow:    true,
		filter:    fi,
		vp:        vp,
		saveDir:   saveDir,
	}
}

// start fetches containers if unknown, otherwise (re)starts the streams.
func (v *logsView) start(d deps) tea.Cmd {
	v.deps = d
	if v.containers == nil {
		v.loading = true
		id, ns, pod, get := v.id, v.namespace, v.pod, d.get
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			obj, err := get.Get(ctx, k8s.Pods, ns, pod)
			if err != nil {
				return containersMsg{id: id, err: err}
			}
			return containersMsg{id: id, names: k8s.Containers(obj)}
		}
	}
	return v.restart()
}

func (v *logsView) stop() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
}

// restart stops any streams, clears lines, and starts streams for the
// selected containers with the current options.
func (v *logsView) restart() tea.Cmd {
	v.stop()
	v.lines = nil
	v.err = nil
	v.endedMsg = ""
	v.loading = true
	targets := v.containers
	if v.selected != "" {
		targets = []string{v.selected}
	}
	if len(targets) == 0 {
		v.loading = false
		v.err = fmt.Errorf("pod %s has no containers", v.pod)
		v.rebuild()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	merged := make(chan k8s.LogEvent, 1024)
	v.events = merged
	v.streams = 0
	var errs []string
	for _, c := range targets {
		ch, err := v.deps.logs.Logs(ctx, v.namespace, v.pod, c, v.opts)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		v.streams++
		go func(ch <-chan k8s.LogEvent) {
			for ev := range ch {
				select {
				case merged <- ev:
				case <-ctx.Done():
					return
				}
			}
		}(ch)
	}
	if len(errs) > 0 {
		v.err = fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	if v.streams == 0 {
		v.loading = false
		v.rebuild()
		return nil
	}
	v.rebuild()
	return v.wait()
}

// wait reads one event, then gathers whatever else arrives within a short
// window so bursts render in one update.
func (v *logsView) wait() tea.Cmd {
	ch, id := v.events, v.id
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return nil
		}
		batch := []k8s.LogEvent{first}
		timer := time.NewTimer(30 * time.Millisecond)
		defer timer.Stop()
		for len(batch) < 2000 {
			select {
			case ev, ok := <-ch:
				if !ok {
					return logLinesMsg{id: id, events: batch}
				}
				batch = append(batch, ev)
			case <-timer.C:
				return logLinesMsg{id: id, events: batch}
			}
		}
		return logLinesMsg{id: id, events: batch}
	}
}

// handleContainers records container names and starts streaming.
func (v *logsView) handleContainers(msg containersMsg) tea.Cmd {
	v.loading = false
	if msg.err != nil {
		v.err = msg.err
		v.rebuild()
		return nil
	}
	v.containers = msg.names
	if v.selected != "" && !contains(v.containers, v.selected) {
		v.selected = ""
	}
	return v.restart()
}

// handleLines applies a batch and re-arms the wait.
func (v *logsView) handleLines(msg logLinesMsg) tea.Cmd {
	v.loading = false
	for _, ev := range msg.events {
		if ev.Ended {
			v.streams--
			if ev.Err != nil {
				v.err = ev.Err
			}
			continue
		}
		v.insert(ev.Line)
	}
	if len(v.lines) > maxLogLines {
		v.lines = v.lines[len(v.lines)-maxLogLines:]
	}
	if v.streams <= 0 {
		v.endedMsg = "stream ended"
		if !v.opts.Follow {
			v.endedMsg = ""
		}
	}
	v.rebuild()
	if v.streams <= 0 || v.cancel == nil {
		return nil
	}
	return v.wait()
}

// insert keeps lines ordered by server timestamp so containers merge
// correctly. New lines almost always belong at the end.
func (v *logsView) insert(l k8s.LogLine) {
	n := len(v.lines)
	if n == 0 || l.Time.IsZero() || !l.Time.Before(v.lines[n-1].Time) {
		v.lines = append(v.lines, l)
		return
	}
	i := sort.Search(n, func(i int) bool { return v.lines[i].Time.After(l.Time) })
	v.lines = append(v.lines, k8s.LogLine{})
	copy(v.lines[i+1:], v.lines[i:])
	v.lines[i] = l
}

// rebuild recomputes the viewport content from lines, filter, and toggles.
func (v *logsView) rebuild() {
	multi := v.selected == "" && len(v.containers) > 1
	nameW := 0
	colorOf := map[string]lipgloss.Style{}
	for i, c := range v.containers {
		nameW = max(nameW, len(c))
		colorOf[c] = lipgloss.NewStyle().Foreground(lipgloss.Color(containerColors[i%len(containerColors)]))
	}
	out := make([]string, 0, len(v.lines))
	for _, l := range v.lines {
		if v.re != nil && !v.re.MatchString(l.Text) {
			continue
		}
		var b strings.Builder
		if v.showTS {
			if l.Time.IsZero() {
				b.WriteString(strings.Repeat(" ", 13))
			} else {
				b.WriteString(logTSStyle.Render(l.Time.Local().Format("15:04:05.000")) + " ")
			}
		}
		if multi {
			st, ok := colorOf[l.Container]
			if !ok {
				st = lipgloss.NewStyle()
			}
			b.WriteString(st.Render(fmt.Sprintf("%-*s", nameW, l.Container)) + " ")
		}
		b.WriteString(v.highlight(l.Text))
		out = append(out, b.String())
	}
	v.shown = len(out)
	if v.endedMsg != "" {
		out = append(out, logEndStyle.Render("── "+v.endedMsg+" ──"))
	}
	v.vp.SoftWrap = v.wrap
	v.vp.SetContentLines(out)
	if v.follow {
		v.vp.GotoBottom()
	}
}

// highlight marks regex matches in a line.
func (v *logsView) highlight(text string) string {
	if v.re == nil {
		return text
	}
	idx := v.re.FindAllStringIndex(text, -1)
	if len(idx) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		if m[1] == m[0] {
			continue
		}
		b.WriteString(text[last:m[0]])
		b.WriteString(logMatchStyle.Render(text[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

func (v *logsView) resize(width, height int) {
	v.width, v.height = width, height
	v.vp.SetWidth(width)
	h := height
	if v.filterActive() {
		h--
	}
	v.vp.SetHeight(max(h, 1))
	if v.follow {
		v.vp.GotoBottom()
	}
}

func (v *logsView) filterActive() bool { return v.typing || v.filter.Value() != "" }

// setFilter compiles the regex (case-insensitive) and re-renders.
func (v *logsView) setFilter(expr string) {
	v.reErr = ""
	if strings.TrimSpace(expr) == "" {
		v.re = nil
		return
	}
	re, err := regexp.Compile("(?i)" + expr)
	if err != nil {
		v.reErr = err.Error()
		return
	}
	v.re = re
}

func (v *logsView) clearFilter() {
	v.typing = false
	v.filter.Blur()
	v.filter.Reset()
	v.re = nil
	v.reErr = ""
}

// setContainer switches the stream to one container ("" for all).
func (v *logsView) setContainer(name string) tea.Cmd {
	v.selected = name
	return v.restart()
}

// setSince changes the time window and restarts.
func (v *logsView) setSince(d time.Duration) tea.Cmd {
	v.opts.Since = d
	if d > 0 {
		v.opts.TailLines = 0 // let the window decide
	} else {
		v.opts.TailLines = defaultTail
	}
	return v.restart()
}

// save writes the shown lines to a file and returns its path.
func (v *logsView) save() (string, error) {
	name := v.pod
	if v.selected != "" {
		name += "-" + v.selected
	}
	path := filepath.Join(v.saveDir, fmt.Sprintf("%s-%s.log", name, time.Now().Format("20060102-150405")))
	var b strings.Builder
	for _, l := range v.lines {
		if v.re != nil && !v.re.MatchString(l.Text) {
			continue
		}
		if !l.Time.IsZero() {
			b.WriteString(l.Time.Format(time.RFC3339Nano) + " ")
		}
		if v.selected == "" && len(v.containers) > 1 {
			b.WriteString("[" + l.Container + "] ")
		}
		b.WriteString(l.Text + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (v *logsView) handleKey(msg tea.KeyPressMsg, width, height int) (tea.Cmd, bool) {
	if v.typing {
		var cmd tea.Cmd
		switch {
		case is(msg, keys.Back):
			v.clearFilter()
			v.resize(width, height)
			v.rebuild()
			return nil, true
		case is(msg, keys.Accept):
			v.typing = false
			v.filter.Blur()
			v.resize(width, height)
			return nil, true
		}
		before := v.filter.Value()
		v.filter, cmd = v.filter.Update(msg)
		if v.filter.Value() != before {
			v.setFilter(v.filter.Value())
			v.rebuild()
		}
		return cmd, true
	}

	switch {
	case is(msg, keys.Filter):
		v.typing = true
		cmd := v.filter.Focus()
		v.resize(width, height)
		return cmd, true
	case is(msg, keys.LogTimestamps):
		v.showTS = !v.showTS
		v.rebuild()
		return nil, true
	case is(msg, keys.LogWrap):
		v.wrap = !v.wrap
		v.rebuild()
		return nil, true
	case is(msg, keys.LogFollow), is(msg, keys.Bottom):
		v.follow = true
		v.vp.GotoBottom()
		return nil, true
	case is(msg, keys.Top):
		v.follow = false
		v.vp.GotoTop()
		return nil, true
	case is(msg, keys.Back):
		if v.filter.Value() != "" {
			v.clearFilter()
			v.resize(width, height)
			v.rebuild()
			return nil, true
		}
		return nil, false
	}
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	// Scrolling away from the bottom pauses follow; reaching it resumes.
	v.follow = v.vp.AtBottom()
	return cmd, true
}

func (v *logsView) render(width, height int) string {
	var body string
	switch {
	case v.err != nil && len(v.lines) == 0:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Padding(1, 2).Render(v.err.Error())
	case v.loading && len(v.lines) == 0:
		body = lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("loading logs for " + v.pod + "…")
	case len(v.lines) == 0 && v.streams > 0:
		body = lipgloss.NewStyle().Faint(true).Padding(1, 2).Render("no log lines yet · following")
	default:
		body = v.vp.View()
	}
	if v.filterActive() {
		body = lipgloss.JoinVertical(lipgloss.Left, v.filterLine(width), body)
	}
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
}

func (v *logsView) filterLine(width int) string {
	right := filterCountStyle.Render(fmt.Sprintf(" %d of %d lines ", v.shown, len(v.lines)))
	if v.reErr != "" {
		right = promptErrStyle.Render(" " + v.reErr + " ")
	} else if v.typing {
		right += filterCountStyle.Render("enter keep · esc clear ")
	} else {
		right += filterCountStyle.Render("esc clear ")
	}
	v.filter.SetWidth(max(width-lipgloss.Width(right)-4, 10))
	left := filterPromptStyle.Render(v.filter.View())
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 0)
	return left + strings.Repeat(" ", gap) + right
}

func (v *logsView) crumbs() []string {
	label := "logs"
	if v.selected != "" {
		label = "logs: " + v.selected
	}
	return []string{k8s.Pods.Name(), v.pod, label}
}

func (v *logsView) capturesInput() bool { return v.typing }

func (v *logsView) status() viewStatus {
	count := fmt.Sprintf("%d lines", len(v.lines))
	if v.re != nil {
		count = fmt.Sprintf("%d of %d lines", v.shown, len(v.lines))
	}
	state := "following"
	switch {
	case v.loading:
		state = "loading"
	case v.streams <= 0:
		state = "ended"
	case !v.follow:
		state = "paused"
	}
	if v.opts.Since > 0 {
		state = "since " + shortDuration(v.opts.Since) + " · " + state
	}
	return viewStatus{count: count, state: state, err: v.err}
}

func (v *logsView) hint() string {
	return "c container  s since  t time  w wrap  / filter  ? keys"
}

func (v *logsView) help() []helpSection {
	km := v.vp.KeyMap
	return []helpSection{
		{"Logs", []key.Binding{keys.LogContainer, keys.LogSince, keys.LogTimestamps, keys.LogWrap, keys.LogFollow, keys.Filter, keys.LogSave, keys.Top, keys.Bottom}},
		{"Scroll", []key.Binding{km.Up, km.Down, km.PageUp, km.PageDown, km.HalfPageUp, km.HalfPageDown}},
	}
}

// sinceChoices are the palette entries for the since picker.
var sinceChoices = []struct {
	label string
	d     time.Duration
}{
	{"all", 0}, {"1m", time.Minute}, {"5m", 5 * time.Minute}, {"15m", 15 * time.Minute},
	{"1h", time.Hour}, {"6h", 6 * time.Hour}, {"24h", 24 * time.Hour},
}

func shortDuration(d time.Duration) string {
	for _, c := range sinceChoices {
		if c.d == d {
			return c.label
		}
	}
	return d.String()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
