package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/ctrl-research/seaglass/internal/config"
	"github.com/ctrl-research/seaglass/internal/k8s"
)

// memStore is an in-memory stateStore.
type memStore struct {
	st    config.State
	saves int
}

func (s *memStore) Load() (config.State, error) { return s.st, nil }
func (s *memStore) Save(st config.State) error  { s.st = st; s.saves++; return nil }

// fakeStreamer records stream requests and lets tests feed updates.
type fakeStreamer struct {
	calls []string
	chans []chan k8s.Update
}

func (f *fakeStreamer) Stream(ctx context.Context, res k8s.Resource, ns string) <-chan k8s.Update {
	f.calls = append(f.calls, res.Name()+"/"+ns)
	ch := make(chan k8s.Update, 4)
	f.chans = append(f.chans, ch)
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}

var deployments = k8s.Resource{
	GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Kind: "Deployment", Namespaced: true, ShortNames: []string{"deploy"},
}
var nodes = k8s.Resource{
	GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Kind: "Node", ShortNames: []string{"no"},
}

// fakeGetter returns a canned object and records requests.
type fakeGetter struct {
	calls []string
	obj   *unstructured.Unstructured
	err   error
}

func (f *fakeGetter) Get(_ context.Context, res k8s.Resource, ns, name string) (*unstructured.Unstructured, error) {
	f.calls = append(f.calls, res.Name()+"/"+ns+"/"+name)
	return f.obj, f.err
}

// fakePatcher records patches and deletes.
type fakePatcher struct {
	patches []string // "res/ns/name: body"
	deletes []string
	err     error
}

func (f *fakePatcher) Patch(_ context.Context, res k8s.Resource, ns, name string, patch []byte) (*unstructured.Unstructured, error) {
	f.patches = append(f.patches, res.Name()+"/"+ns+"/"+name+": "+string(patch))
	return &unstructured.Unstructured{}, f.err
}

func (f *fakePatcher) Delete(_ context.Context, res k8s.Resource, ns, name string, grace *int64) error {
	entry := res.Name() + "/" + ns + "/" + name
	if grace != nil {
		entry += fmt.Sprintf(" grace=%d", *grace)
	}
	f.deletes = append(f.deletes, entry)
	return f.err
}

// fakeLogger hands out channels the test controls, keyed by container.
type fakeLogger struct {
	calls []string // "ns/pod/container since=.. tail=.."
	chans map[string]chan k8s.LogEvent
}

func (f *fakeLogger) Logs(ctx context.Context, ns, pod, container string, opts k8s.LogOptions) (<-chan k8s.LogEvent, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s/%s/%s since=%s tail=%d", ns, pod, container, opts.Since, opts.TailLines))
	if f.chans == nil {
		f.chans = map[string]chan k8s.LogEvent{}
	}
	ch := make(chan k8s.LogEvent, 64)
	f.chans[container] = ch
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}

// fakeExecer simulates a remote shell: it prints a prompt, echoes every
// byte it reads from stdin, and exits when the context ends or it reads
// "exit\r". RunCommand answers the user probe.
type fakeExecer struct {
	mu    sync.Mutex
	calls []string
	user  string
}

func (f *fakeExecer) Exec(ctx context.Context, o k8s.ExecOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, o.Namespace+"/"+o.Pod+"/"+o.Container)
	f.mu.Unlock()
	if !o.TTY {
		return nil
	}
	_, _ = o.Stdout.Write([]byte("$ "))
	buf := make([]byte, 64)
	var line []byte
	for {
		n, err := o.Stdin.Read(buf)
		if err != nil {
			return nil
		}
		for _, b := range buf[:n] {
			if b == 0x7f || b == '\b' {
				if len(line) > 0 {
					line = line[:len(line)-1]
					_, _ = o.Stdout.Write([]byte("\b \b"))
				}
				continue
			}
			if b == '\r' {
				if string(line) == "exit" {
					_, _ = o.Stdout.Write([]byte("\r\nbye\r\n"))
					return nil
				}
				line = nil
				_, _ = o.Stdout.Write([]byte("\r\n$ "))
				continue
			}
			line = append(line, b)
			_, _ = o.Stdout.Write([]byte{b})
		}
	}
}

func (f *fakeExecer) RunCommand(context.Context, string, string, string, []string) (string, error) {
	if f.user == "" {
		return "root", nil
	}
	return f.user, nil
}

// rv asserts the top view is a table view.
func rv(m Model) *resourceView { return m.top().(*resourceView) }

func newTest(t *testing.T) (Model, *fakeStreamer) {
	t.Helper()
	m, fs, _ := newTestFull(t)
	return m, fs
}

func newTestFull(t *testing.T) (Model, *fakeStreamer, *fakeGetter) {
	t.Helper()
	fs := &fakeStreamer{}
	fg := &fakeGetter{obj: &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "b", "namespace": "default", "uid": "2", "labels": map[string]any{"app": "web"}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "app"}, map[string]any{"name": "sidecar"}}},
		"status":   map[string]any{"phase": "Pending"},
	}}}
	m := New(Options{
		Client:    &k8s.Client{Context: "test-ctx", Namespace: "default"},
		Namespace: "default",
		Resource:  k8s.Pods,
		SaveDir:   t.TempDir(),
		streamer:  fs,
		getter:    fg,
		patcher:   &fakePatcher{},
		logger:    &fakeLogger{},
		execer:    &fakeExecer{},
		contexts:  []string{"test-ctx", "other-ctx"},
	})
	// Init returns a batch; run the stream start directly instead so tests
	// stay synchronous.
	rv(m).start(m.deps)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = mm.(Model)
	mm, _ = m.Update(resourcesMsg{resources: []k8s.Resource{k8s.Pods, deployments, nodes, k8s.Namespaces}})
	m = mm.(Model)
	mm, _ = m.Update(namespacesMsg{names: []string{"default", "kube-system"}})
	return mm.(Model), fs, fg
}

func snap() k8s.Snapshot {
	return k8s.Snapshot{
		Columns: []k8s.Column{{Name: "NAME"}, {Name: "STATUS"}, {Name: "IP", Priority: 1}},
		Rows: []k8s.Row{
			{Name: "a", UID: "1", Cells: []string{"a", "Running", "10.0.0.1"}},
			{Name: "b", UID: "2", Cells: []string{"b", "Pending", "10.0.0.2"}},
			{Name: "c", UID: "3", Cells: []string{"c", "Running", "10.0.0.3"}},
		},
	}
}

func kp(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+]":
		return tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func press(m Model, keys ...string) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		var mm tea.Model
		mm, cmd = m.Update(kp(k))
		m = mm.(Model)
	}
	return m, cmd
}

func typeStr(m Model, s string) Model {
	for _, r := range s {
		m, _ = press(m, string(r))
	}
	return m
}

func feed(m Model, u k8s.Update) Model {
	mm, _ := m.Update(updateMsg{id: rv(m).id, Update: u})
	return mm.(Model)
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m, _ := newTest(t)
		_, cmd := press(m, k)
		if cmd == nil {
			t.Fatalf("%s: expected quit cmd", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s: expected QuitMsg", k)
		}
	}
}

func TestRendersRowsAndStatus(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	out := m.View().Content
	for _, want := range []string{"NAME", "STATUS", "Running", "Pending", "test-ctx", "pods", "3 rows", "live"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q\n%s", want, out)
		}
	}
	if lines := strings.Count(out, "\n") + 1; lines != 24 {
		t.Errorf("view has %d lines, want 24", lines)
	}
}

func TestSelectionSurvivesUpdate(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "down")
	if rv(m).selectedKey() != "2" {
		t.Fatalf("selected %q after down, want b", rv(m).selectedKey())
	}
	s := snap()
	s.Rows = append([]k8s.Row{{Name: "0", UID: "0", Cells: []string{"0", "Running", ""}}}, s.Rows...)
	m = feed(m, k8s.Update{Snapshot: s, Status: k8s.StatusLive})
	if rv(m).selectedKey() != "2" {
		t.Errorf("selection moved to %q after insert above", rv(m).selectedKey())
	}
}

func TestPaletteResourcePushAndPop(t *testing.T) {
	m, fs := newTest(t)
	m, _ = press(m, ":")
	if !m.palette.open {
		t.Fatal("palette should open on ':'")
	}
	m = typeStr(m, "deploy")
	if it, ok := m.palette.selected(); !ok || it.Label != "deployments" {
		t.Fatalf("top match for 'deploy' = %+v", it)
	}
	m, _ = press(m, "enter")
	if m.palette.open {
		t.Error("palette should close on enter")
	}
	if len(m.stack) != 2 || rv(m).res.Name() != "deployments" {
		t.Fatalf("stack = %d, top = %s", len(m.stack), rv(m).res.Name())
	}
	if got := fs.calls[len(fs.calls)-1]; got != "deployments/default" {
		t.Errorf("stream started for %q", got)
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "test-ctx › default › deployments") {
		t.Errorf("breadcrumbs should show scope only:\n%s", out)
	}
	if strings.Contains(out, "pods › deployments") {
		t.Errorf("breadcrumbs must not show view history:\n%s", out)
	}
	if !strings.Contains(out, "esc back to pods") {
		t.Errorf("hint should name the previous view:\n%s", out)
	}

	m, _ = press(m, "esc")
	if len(m.stack) != 1 || rv(m).res.Name() != "pods" {
		t.Fatalf("esc should pop back to pods, got %s", rv(m).res.Name())
	}
	if got := fs.calls[len(fs.calls)-1]; got != "pods/default" {
		t.Errorf("pods stream not restarted after pop: %q", got)
	}
}

func TestClusterScopedResourceIgnoresNamespace(t *testing.T) {
	m, fs := newTest(t)
	m, _ = press(m, ":")
	m = typeStr(m, "nodes")
	m, _ = press(m, "enter")
	if got := fs.calls[len(fs.calls)-1]; got != "nodes/" {
		t.Errorf("nodes should stream cluster-wide, got %q", got)
	}
	if !strings.Contains(m.View().Content, "default") {
		t.Error("status bar should still show the working namespace")
	}
}

func TestPaletteNamespaceResetsStack(t *testing.T) {
	m, fs := newTest(t)
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	m, _ = press(m, ":")
	m = typeStr(m, "ns kube")
	if it, ok := m.palette.selected(); !ok || it.Kind != itemNamespace || it.Name != "kube-system" {
		t.Fatalf("'ns kube' should select the kube-system namespace, got %+v", it)
	}
	m, _ = press(m, "enter")
	if m.namespace != "kube-system" || len(m.stack) != 1 || rv(m).res.Name() != "deployments" {
		t.Fatalf("ns=%s stack=%d top=%s", m.namespace, len(m.stack), rv(m).res.Name())
	}
	if got := fs.calls[len(fs.calls)-1]; got != "deployments/kube-system" {
		t.Errorf("stream = %q", got)
	}
}

func TestStaleUpdateIgnored(t *testing.T) {
	m, _ := newTest(t)
	oldID := rv(m).id
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	mm, _ := m.Update(updateMsg{id: oldID, Update: k8s.Update{Snapshot: snap(), Status: k8s.StatusLive}})
	m = mm.(Model)
	if len(rv(m).snapshot.Rows) != 0 {
		t.Error("update for a popped view leaked into the top view")
	}
}

func TestPaletteEscClosesWithoutPopping(t *testing.T) {
	m, _ := newTest(t)
	m, _ = press(m, ":", "esc")
	if m.palette.open || len(m.stack) != 1 {
		t.Error("esc should only close the palette")
	}
}

func TestPaletteFilterMultiTerm(t *testing.T) {
	m, _ := newTest(t)
	m, _ = press(m, ":")
	m = typeStr(m, "ctx other")
	it, ok := m.palette.selected()
	if !ok || it.Kind != itemContext || it.Name != "other-ctx" {
		t.Errorf("'ctx other' selected %+v", it)
	}
	m = typeStr(m, "zzzz")
	if _, ok := m.palette.selected(); ok {
		t.Error("expected no matches")
	}
	if !strings.Contains(m.View().Content, "no matches") {
		t.Error("view should say no matches")
	}
}

func TestPaletteAllNamespaces(t *testing.T) {
	m, fs := newTest(t)
	m, _ = press(m, ":")
	m = typeStr(m, "ns all")
	it, ok := m.palette.selected()
	if !ok || it.Kind != itemNamespace || it.Name != "" || it.Label != "all namespaces" {
		t.Fatalf("'ns all' selected %+v", it)
	}
	m, _ = press(m, "enter")
	if m.namespace != "" {
		t.Fatalf("namespace = %q, want all", m.namespace)
	}
	if got := fs.calls[len(fs.calls)-1]; got != "pods/" {
		t.Errorf("stream = %q, want pods across all namespaces", got)
	}
	if out := stripANSI(m.View().Content); !strings.Contains(out, "test-ctx › all › pods") {
		t.Errorf("status bar should show 'all':\n%s", out)
	}
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func TestRowFilter(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "/")
	if !rv(m).typing {
		t.Fatal("/ should start the filter")
	}
	m = typeStr(m, "pend")
	if got := len(rv(m).filtered); got != 1 || rv(m).filtered[0].Name != "b" {
		t.Fatalf("filter 'pend' -> %d rows (%+v)", got, rv(m).filtered)
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "1 of 3") {
		t.Errorf("filter line should show the count:\n%s", out)
	}
	if strings.Contains(out, "Running") {
		t.Errorf("filtered-out rows still rendered:\n%s", out)
	}
	// q while typing is text, not quit.
	m, cmd := press(m, "q")
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("q while typing a filter must not quit")
		}
	}
	if rv(m).filter.Value() != "pendq" {
		t.Errorf("filter value = %q", rv(m).filter.Value())
	}
	if len(rv(m).filtered) != 0 || !strings.Contains(stripANSI(m.View().Content), "0 of 3") {
		t.Error("no rows should match 'pendq'")
	}
	// Backspace, then enter keeps the filter applied without focus.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = mm.(Model)
	m, _ = press(m, "enter")
	if rv(m).typing || len(rv(m).filtered) != 1 {
		t.Fatalf("enter should keep filter: typing=%v rows=%d", rv(m).typing, len(rv(m).filtered))
	}
	if !strings.Contains(stripANSI(m.View().Content), "1 of 3 rows") {
		t.Error("status bar should show filtered count")
	}
	// Live update keeps the filter applied.
	s := snap()
	s.Rows = append(s.Rows, k8s.Row{Name: "d", UID: "4", Cells: []string{"d", "Pending", ""}})
	m = feed(m, k8s.Update{Snapshot: s, Status: k8s.StatusLive})
	if len(rv(m).filtered) != 2 {
		t.Errorf("filter not reapplied on update: %d rows", len(rv(m).filtered))
	}
	// esc clears the filter instead of popping.
	m, _ = press(m, "esc")
	if rv(m).filterActive() || len(rv(m).filtered) != 4 || len(m.stack) != 1 {
		t.Errorf("esc should clear the filter: active=%v rows=%d stack=%d", rv(m).filterActive(), len(rv(m).filtered), len(m.stack))
	}
}

func TestFilterEscWhileTypingClears(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "/")
	m = typeStr(m, "run")
	m, _ = press(m, "esc")
	if rv(m).filterActive() || len(rv(m).filtered) != 3 {
		t.Error("esc while typing should clear and close the filter")
	}
	if lines := strings.Count(m.View().Content, "\n") + 1; lines != 24 {
		t.Errorf("view has %d lines after clearing filter, want 24", lines)
	}
}

func TestSelectionFollowsFilteredRows(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "down", "down") // on c
	m, _ = press(m, "/")
	m = typeStr(m, "run") // a and c match
	if r, ok := rv(m).selectedRow(); !ok || r.Name != "c" {
		t.Errorf("selection should stay on c, got %+v", r)
	}
}

func TestRowFilterIsSubstringNotFuzzy(t *testing.T) {
	m, _ := newTest(t)
	s := snap()
	s.Rows = []k8s.Row{
		{Name: "coredns-1", UID: "1", Cells: []string{"coredns-1", "Running", "10.0.0.1"}},
		{Name: "etcd-control-plane", UID: "2", Cells: []string{"etcd-control-plane", "Running", "10.0.0.2"}},
		{Name: "kube-proxy", UID: "3", Cells: []string{"kube-proxy", "Pending", "10.0.0.3"}},
	}
	m = feed(m, k8s.Update{Snapshot: s, Status: k8s.StatusLive})
	m, _ = press(m, "/")
	m = typeStr(m, "core")
	if got := len(rv(m).filtered); got != 1 || rv(m).filtered[0].Name != "coredns-1" {
		t.Errorf("'core' should match only coredns, got %d rows", got)
	}
	m, _ = press(m, "esc", "/")
	m = typeStr(m, "RUN 10.0.0.2")
	if got := len(rv(m).filtered); got != 1 || rv(m).filtered[0].Name != "etcd-control-plane" {
		t.Errorf("multi-term case-insensitive filter got %d rows", got)
	}
	m, _ = press(m, "esc", "/")
	m = typeStr(m, "cre") // scattered letters of coredns must not match
	if got := len(rv(m).filtered); got != 0 {
		t.Errorf("'cre' matched %d rows; substring filter expected", got)
	}
}

// openObject drives the fetch for the top object view synchronously.
func openObject(m Model) Model {
	ov := m.top().(*objectView)
	cmd := ov.start(m.deps)
	mm, _ := m.Update(cmd())
	return mm.(Model)
}

func TestEnterOpensDetailAndEscReturns(t *testing.T) {
	m, fs, fg := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "down") // b
	m, cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("enter should start a fetch")
	}
	ov, ok := m.top().(*objectView)
	if !ok || ov.name != "b" || ov.mode != modeDetail {
		t.Fatalf("top = %T %+v", m.top(), m.top())
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "test-ctx › default › pods › b") {
		t.Errorf("crumbs should include the object:\n%s", out)
	}
	if !strings.Contains(out, "loading Pod b") {
		t.Errorf("should show loading state:\n%s", out)
	}

	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if got := fg.calls[len(fg.calls)-1]; got != "pods/default/b" {
		t.Errorf("fetched %q", got)
	}
	out = stripANSI(m.View().Content)
	for _, want := range []string{"Kind:         Pod", "app=web", "phase:               Pending", "esc back to pods"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}

	m, _ = press(m, "y")
	out = stripANSI(m.View().Content)
	if !strings.Contains(out, "apiVersion: v1") || !strings.Contains(out, "phase: Pending") {
		t.Errorf("yaml mode missing content:\n%s", out)
	}
	if !strings.Contains(out, "yaml") {
		t.Errorf("status should say yaml:\n%s", out)
	}

	m, _ = press(m, "esc")
	if _, ok := m.top().(*resourceView); !ok || len(m.stack) != 1 {
		t.Fatalf("esc should return to the table, stack=%d", len(m.stack))
	}
	if got := fs.calls[len(fs.calls)-1]; got != "pods/default" {
		t.Errorf("table stream not resumed: %q", got)
	}
	if rv(m).selectedKey() != "2" {
		t.Errorf("selection lost on return: %q", rv(m).selectedKey())
	}
}

func TestYOpensYAMLDirectly(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "y")
	m = openObject(m)
	if ov := m.top().(*objectView); ov.mode != modeYAML {
		t.Errorf("mode = %v", ov.mode)
	}
	if lines := strings.Count(m.View().Content, "\n") + 1; lines != 24 {
		t.Errorf("view has %d lines, want 24", lines)
	}
}

func TestObjectFetchError(t *testing.T) {
	m, _, fg := newTestFull(t)
	fg.err = errors.New("pods \"b\" is forbidden")
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "enter")
	m = openObject(m)
	if !strings.Contains(stripANSI(m.View().Content), "forbidden") {
		t.Error("fetch error should be shown")
	}
}

func TestEnterWithNoRowsDoesNothing(t *testing.T) {
	m, _ := newTest(t)
	m, _ = press(m, "enter", "d", "y")
	if len(m.stack) != 1 {
		t.Error("no row selected, nothing should open")
	}
}

func TestStaleObjectMsgIgnored(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "enter")
	m, _ = press(m, "esc") // back before the fetch lands
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if _, ok := m.top().(*resourceView); !ok {
		t.Error("stale object message changed the top view")
	}
}

func TestPaletteFromObjectViewPushesTable(t *testing.T) {
	m, fs, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "enter")
	m = openObject(m)
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	if rv(m).res.Name() != "deployments" || fs.calls[len(fs.calls)-1] != "deployments/default" {
		t.Errorf("palette from object view: top=%s calls=%v", rv(m).res.Name(), fs.calls)
	}
}

func TestHeaderShownAndHidden(t *testing.T) {
	m, _ := newTest(t)
	mm, _ := m.Update(versionMsg{version: "v1.30.0"})
	m = mm.(Model)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	out := stripANSI(m.View().Content)
	for _, want := range []string{"/ ___|  ___", "context: test-ctx", "namespace: default", "k8s: v1.30.0", "────"} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q:\n%s", want, out)
		}
	}
	if lines := strings.Count(out, "\n") + 1; lines != 24 {
		t.Errorf("view has %d lines, want 24", lines)
	}
	// Table must still show rows beneath the header.
	if !strings.Contains(out, "Running") {
		t.Error("table hidden by header")
	}

	mm, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m = mm.(Model)
	out = stripANSI(m.View().Content)
	if strings.Contains(out, "|___/") {
		t.Error("header should hide on a short terminal")
	}
	if lines := strings.Count(out, "\n") + 1; lines != 12 {
		t.Errorf("view has %d lines, want 12", lines)
	}
	if !strings.Contains(out, "Running") {
		t.Error("table should still render on a short terminal")
	}
}

func TestPersistsPositionChanges(t *testing.T) {
	store := &memStore{}
	fs := &fakeStreamer{}
	m := New(Options{
		Client:    &k8s.Client{Context: "test-ctx", Namespace: "default"},
		Namespace: "default",
		Resource:  k8s.Pods,
		streamer:  fs,
		getter:    &fakeGetter{},
		contexts:  []string{"test-ctx"},
		State:     store,
	})
	rv(m).start(m.deps)
	run := func(msg tea.Msg) {
		mm, cmd := m.Update(msg)
		m = mm.(Model)
		execCmd(cmd)
	}
	run(tea.WindowSizeMsg{Width: 100, Height: 24})
	if store.saves != 1 {
		t.Fatalf("initial position should be saved once, got %d", store.saves)
	}
	run(resourcesMsg{resources: []k8s.Resource{k8s.Pods, deployments}})
	run(namespacesMsg{names: []string{"default", "kube-system"}})
	run(kp("j")) // cursor moves must not save
	if store.saves != 1 {
		t.Errorf("unrelated updates saved state: %d", store.saves)
	}

	run(kp(":"))
	for _, r := range "ns kube-sys" {
		run(kp(string(r)))
	}
	run(kp("enter"))
	if store.saves != 2 {
		t.Fatalf("namespace change should save, got %d saves", store.saves)
	}
	cs, ok := store.st.For("test-ctx")
	if !ok || cs.Namespace != "kube-system" || cs.AllNamespaces || cs.Resource.GVR.Resource != "pods" {
		t.Errorf("saved %+v", cs)
	}

	run(kp(":"))
	for _, r := range "deploy" {
		run(kp(string(r)))
	}
	run(kp("enter"))
	cs, _ = store.st.For("test-ctx")
	if store.saves != 3 || cs.Resource.GVR.Resource != "deployments" {
		t.Errorf("resource change: saves=%d resource=%+v", store.saves, cs.Resource)
	}

	run(kp("esc")) // back to pods
	cs, _ = store.st.For("test-ctx")
	if store.saves != 4 || cs.Resource.GVR.Resource != "pods" {
		t.Errorf("pop should save the resumed resource: saves=%d resource=%+v", store.saves, cs.Resource)
	}
	if store.st.LastContext != "test-ctx" {
		t.Errorf("last context = %q", store.st.LastContext)
	}
}

// execCmd runs a command and, for batches, each member, giving each a
// short window. Stream waits block forever on the fake streamer and are
// simply abandoned.
func execCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				execCmd(c)
			}
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSortPickerAndReverse(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "s")
	if !m.palette.open {
		t.Fatal("s should open the sort picker")
	}
	if it, ok := m.palette.selected(); !ok || it.Kind != itemSort || it.Label != "NAME" {
		t.Fatalf("first sort item = %+v", it)
	}
	m = typeStr(m, "status")
	m, _ = press(m, "enter")
	if rv(m).sortCol != 1 || rv(m).sortDesc {
		t.Fatalf("sort = col %d desc %v", rv(m).sortCol, rv(m).sortDesc)
	}
	// Pending < Running, so b sorts first.
	if rv(m).filtered[0].Name != "b" {
		t.Errorf("asc by STATUS should put b first, got %s", rv(m).filtered[0].Name)
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "STATUS ▲") {
		t.Errorf("header should mark the sorted column:\n%s", out)
	}
	m, _ = press(m, "S")
	if !rv(m).sortDesc || rv(m).filtered[0].Name == "b" {
		t.Errorf("S should reverse: desc=%v first=%s", rv(m).sortDesc, rv(m).filtered[0].Name)
	}
	if !strings.Contains(stripANSI(m.View().Content), "STATUS ▼") {
		t.Error("header should show descending arrow")
	}
	// Palette is back to global items after the picker closes.
	m, _ = press(m, ":")
	if it, _ := m.palette.selected(); it.Kind == itemSort {
		t.Error("global palette should not show sort items")
	}
	m, _ = press(m, "esc")
	// Live updates keep the sort.
	sn := snap()
	sn.Rows = append(sn.Rows, k8s.Row{Name: "z", UID: "9", Cells: []string{"z", "Succeeded", ""}})
	m = feed(m, k8s.Update{Snapshot: sn, Status: k8s.StatusLive})
	if rv(m).filtered[0].Name != "z" {
		t.Errorf("desc by STATUS after update should put Succeeded first, got %s", rv(m).filtered[0].Name)
	}
	// Server order restores.
	m, _ = press(m, "s")
	m = typeStr(m, "server")
	m, _ = press(m, "enter")
	if rv(m).sortCol != -1 || rv(m).filtered[0].Name != "a" {
		t.Errorf("server order not restored: col=%d first=%s", rv(m).sortCol, rv(m).filtered[0].Name)
	}
}

func TestColumnModeCycle(t *testing.T) {
	m, _ := newTest(t)
	// NAME(4)+STATUS(7) with padding is 15; IP needs 10 more.
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 22, Height: 24})
	m = mm.(Model)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	if strings.Contains(stripANSI(m.View().Content), "IP") {
		t.Fatal("auto mode at width 22 should drop the IP column")
	}
	m, _ = press(m, "w")
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "IP") || !strings.Contains(out, "wide") {
		t.Errorf("wide mode should force IP and say wide:\n%s", out)
	}
	m, _ = press(m, "w")
	if !strings.Contains(stripANSI(m.View().Content), "narrow") {
		t.Error("second w should be narrow")
	}
	m, _ = press(m, "w")
	if rv(m).colMode != 0 {
		t.Error("third w should return to auto")
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "?")
	if !m.showHelp {
		t.Fatal("? should open help")
	}
	out := stripANSI(m.View().Content)
	for _, want := range []string{"Global", "command palette", "Table", "sort by column", "cycle columns", "Move", "Palette / filter", "? or esc closes help"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Running") {
		t.Error("table should be hidden behind help")
	}
	if lines := strings.Count(out, "\n") + 1; lines != 24 {
		t.Errorf("view has %d lines, want 24", lines)
	}
	// q closes help rather than quitting.
	m, cmd := press(m, "q")
	if m.showHelp || cmd != nil {
		t.Error("q should close help without quitting")
	}
	if !strings.Contains(stripANSI(m.View().Content), "Running") {
		t.Error("table should return after help closes")
	}
	// Help on an object view shows object bindings.
	m, _ = press(m, "enter")
	m = openObject(m)
	m, _ = press(m, "?")
	out = stripANSI(m.View().Content)
	if !strings.Contains(out, "Object") || !strings.Contains(out, "copy yaml") || !strings.Contains(out, "Scroll") {
		t.Errorf("object help missing:\n%s", out)
	}
	m, _ = press(m, "esc")
	if m.showHelp || len(m.stack) != 2 {
		t.Error("esc should close help, not pop the view")
	}
}

func TestPaletteQuitAndHelpActions(t *testing.T) {
	for _, q := range []string{"quit", "exit", ":q"} {
		m, _ := newTest(t)
		m, _ = press(m, ":")
		m = typeStr(m, q)
		it, ok := m.palette.selected()
		if !ok || it.Kind != itemAction || it.Name != actionQuit {
			t.Fatalf("%q selected %+v", q, it)
		}
		_, cmd := press(m, "enter")
		if cmd == nil {
			t.Fatalf("%q: expected a quit cmd", q)
		}
		if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
			// choose returns tea.Quit directly, but it may be batched.
			if b, ok := cmd().(tea.BatchMsg); ok {
				for _, c := range b {
					if c != nil {
						if _, isQuit = c().(tea.QuitMsg); isQuit {
							break
						}
					}
				}
			}
			if !isQuit {
				t.Errorf("%q: expected QuitMsg", q)
			}
		}
	}
	m, _ := newTest(t)
	m, _ = press(m, ":")
	m = typeStr(m, "help")
	m, _ = press(m, "enter")
	if !m.showHelp {
		t.Error("help action should open the overlay")
	}
}

func fp(m Model) *fakePatcher { return m.deps.patch.(*fakePatcher) }

// runResult executes an action cmd and feeds its result back.
func runResult(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	mm, _ := m.Update(cmd())
	return mm.(Model)
}

func TestRestartActionOnDeployment(t *testing.T) {
	m, _ := newTest(t)
	// Pods do not offer restart.
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "r")
	if cmd != nil || len(fp(m).patches) != 0 {
		t.Fatal("r on a pod must not patch anything")
	}
	// Switch to deployments and restart the selected one.
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	m = feed(m, k8s.Update{Snapshot: k8s.Snapshot{
		Columns: []k8s.Column{{Name: "NAME"}},
		Rows:    []k8s.Row{{Name: "web", Namespace: "default", UID: "w", Cells: []string{"web"}}},
	}, Status: k8s.StatusLive})
	m, cmd = press(m, "r")
	if cmd != nil || m.confirm == nil {
		t.Fatal("r on a deployment should ask for confirmation")
	}
	if !strings.Contains(stripANSI(m.View().Content), "rollout restart Deployment default/web?") {
		t.Errorf("confirm text:\n%s", stripANSI(m.View().Content))
	}
	m, cmd = press(m, "y")
	if cmd == nil {
		t.Fatal("y should run restart")
	}
	m = runResult(m, cmd)
	if len(fp(m).patches) != 1 || !strings.HasPrefix(fp(m).patches[0], "deployments/default/web: ") {
		t.Fatalf("patches = %v", fp(m).patches)
	}
	if !strings.Contains(fp(m).patches[0], `"kubectl.kubernetes.io/restartedAt":"`) {
		t.Errorf("restart patch body wrong: %s", fp(m).patches[0])
	}
	if !strings.Contains(stripANSI(m.View().Content), "rollout restart: Deployment default/web") {
		t.Errorf("notice missing:\n%s", stripANSI(m.View().Content))
	}
	// Notice clears on its tick.
	mm, _ := m.Update(clearNoticeMsg{seq: m.noticeSeq})
	m = mm.(Model)
	if m.notice != "" {
		t.Error("notice should clear")
	}
}

func TestDeleteConfirms(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "down") // b
	m, cmd := press(m, "ctrl+d")
	if cmd != nil || m.confirm == nil {
		t.Fatal("delete should ask for confirmation first")
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "delete object Pod default/b?") {
		t.Errorf("confirm dialog missing:\n%s", out)
	}
	m, cmd = press(m, "n")
	if m.confirm != nil || cmd != nil || len(fp(m).deletes) != 0 {
		t.Fatal("n should cancel")
	}
	m, _ = press(m, "ctrl+d")
	out = stripANSI(m.View().Content)
	if !strings.Contains(out, "f force (grace period 0): off") {
		t.Errorf("force toggle missing:\n%s", out)
	}
	m, _ = press(m, "f")
	if !m.confirm.force || !strings.Contains(stripANSI(m.View().Content), "force (grace period 0): ON") {
		t.Error("f should turn force on")
	}
	m, _ = press(m, "f")
	if m.confirm.force {
		t.Error("f again should turn force off")
	}
	m, _ = press(m, "f")
	m, cmd = press(m, "y")
	if cmd == nil {
		t.Fatal("y should run the delete")
	}
	m = runResult(m, cmd)
	if len(fp(m).deletes) != 1 || fp(m).deletes[0] != "pods/default/b grace=0" {
		t.Errorf("deletes = %v", fp(m).deletes)
	}
	if !strings.Contains(stripANSI(m.View().Content), "force deleted Pod default/b") {
		t.Error("notice missing after delete")
	}
}

func TestRestartHasNoForceToggle(t *testing.T) {
	m := onDeployments(t)
	m, _ = press(m, "r")
	if strings.Contains(stripANSI(m.View().Content), "force") {
		t.Error("restart should not offer force")
	}
	m, _ = press(m, "f") // ignored
	if m.confirm == nil || m.confirm.force {
		t.Error("f must be ignored when the action has no force option")
	}
}

func TestActionErrorShown(t *testing.T) {
	m, _ := newTest(t)
	fp(m).err = errors.New("pods \"b\" is forbidden")
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "ctrl+d")
	m, cmd := press(m, "y")
	m = runResult(m, cmd)
	if !strings.Contains(stripANSI(m.View().Content), "forbidden") {
		t.Error("action error should show in the status bar")
	}
	m, _ = press(m, "j")
	if m.err != nil {
		t.Error("next key should clear the error")
	}
}

func TestPaletteListsActionsForSelection(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, ":")
	if it, ok := m.palette.selected(); !ok || it.Kind != itemAction || it.Label != "delete" {
		t.Fatalf("first palette item should be the delete action for the row, got %+v", it)
	}
	if !strings.Contains(stripANSI(m.View().Content), "delete object · a") {
		t.Error("action detail should name the row")
	}
	m = typeStr(m, "restart")
	if _, ok := m.palette.selected(); ok {
		t.Error("restart should not be offered for pods")
	}
	m, _ = press(m, "esc")
	// Help lists the applicable actions.
	m, _ = press(m, "?")
	if !strings.Contains(stripANSI(m.View().Content), "Actions on Pod") {
		t.Error("help should list actions for the resource")
	}
}

func deploySnap() k8s.Snapshot {
	return k8s.Snapshot{
		Columns: []k8s.Column{{Name: "Name"}, {Name: "Ready"}, {Name: "Up-to-date"}},
		Rows:    []k8s.Row{{Name: "web", Namespace: "default", UID: "w", Cells: []string{"web", "3/3", "3"}}},
	}
}

func onDeployments(t *testing.T) Model {
	t.Helper()
	m, _ := newTest(t)
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	return feed(m, k8s.Update{Snapshot: deploySnap(), Status: k8s.StatusLive})
}

func TestScalePromptDefaultsAndPatches(t *testing.T) {
	m := onDeployments(t)
	m, _ = press(m, "=")
	if !m.prompt.open || m.prompt.input.Value() != "3" {
		t.Fatalf("scale should prompt with the desired count: open=%v value=%q", m.prompt.open, m.prompt.input.Value())
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "scale Deployment default/web") || !strings.Contains(out, "replicas:") {
		t.Errorf("prompt line missing:\n%s", out)
	}
	if lines := strings.Count(out, "\n") + 1; lines != 24 {
		t.Errorf("view has %d lines with prompt, want 24", lines)
	}
	// Typing q must not quit while the prompt is open; clear and type 5.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = mm.(Model)
	m = typeStr(m, "x")
	m, cmd := press(m, "enter")
	if cmd != nil || !m.prompt.open || m.prompt.errText == "" {
		t.Fatal("invalid replicas should keep the prompt open with an error")
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = mm.(Model)
	m = typeStr(m, "5")
	m, _ = press(m, "enter")
	if m.prompt.open || m.confirm == nil {
		t.Fatal("valid input should close the prompt and ask for confirmation")
	}
	if out := stripANSI(m.View().Content); !strings.Contains(out, "scale Deployment default/web to 5 replicas?") {
		t.Errorf("confirm text:\n%s", out)
	}
	m, cmd = press(m, "y")
	if cmd == nil {
		t.Fatal("y should run the scale")
	}
	execCmdInto(&m, cmd)
	if len(fp(m).patches) != 1 || !strings.HasSuffix(fp(m).patches[0], `{"spec":{"replicas":5}}`) {
		t.Errorf("patches = %v", fp(m).patches)
	}
	if !strings.Contains(stripANSI(m.View().Content), "scale Deployment default/web to 5 replicas") {
		t.Errorf("notice missing:\n%s", stripANSI(m.View().Content))
	}
}

func TestScaleUpDownFromTable(t *testing.T) {
	m := onDeployments(t)
	m, _ = press(m, "+")
	m, cmd := press(m, "y")
	execCmdInto(&m, cmd)
	m, _ = press(m, "-")
	m, cmd = press(m, "y")
	execCmdInto(&m, cmd)
	if len(fp(m).patches) != 2 || !strings.HasSuffix(fp(m).patches[0], `{"spec":{"replicas":4}}`) || !strings.HasSuffix(fp(m).patches[1], `{"spec":{"replicas":2}}`) {
		t.Errorf("patches = %v", fp(m).patches)
	}
	// Without a readable desired count, +/- report an error instead.
	sn := deploySnap()
	sn.Columns = []k8s.Column{{Name: "Name"}}
	sn.Rows[0].Cells = []string{"web"}
	m = feed(m, k8s.Update{Snapshot: sn, Status: k8s.StatusLive})
	m, _ = press(m, "+")
	m, cmd = press(m, "y")
	execCmdInto(&m, cmd)
	if m.err == nil || !strings.Contains(m.err.Error(), "desired replicas") {
		t.Errorf("expected a readable error, got %v", m.err)
	}
	if len(fp(m).patches) != 2 {
		t.Error("no patch should be sent without a desired count")
	}
}

func TestScalePromptEscCancels(t *testing.T) {
	m := onDeployments(t)
	m, _ = press(m, "=")
	m, _ = press(m, "esc")
	if m.prompt.open || len(m.stack) != 2 || len(fp(m).patches) != 0 {
		t.Error("esc should cancel the prompt only")
	}
	if !strings.Contains(stripANSI(m.View().Content), "3/3") {
		t.Error("table should be back after cancel")
	}
}

func TestPodsDoNotOfferScale(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "=")
	if cmd != nil || m.prompt.open {
		t.Error("= on a pod must do nothing")
	}
}

// execCmdInto runs a cmd (expanding batches) and feeds resulting messages
// back into the model.
func execCmdInto(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				execCmdInto(m, c)
			}
			return
		}
		if msg != nil {
			mm, _ := m.Update(msg)
			*m = mm.(Model)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func fl(m Model) *fakeLogger { return m.deps.logs.(*fakeLogger) }

func ts(sec int) time.Time { return time.Date(2026, 9, 14, 12, 0, sec, 0, time.UTC) }

// openLogs opens logs on the selected pod and runs the container fetch
// and stream start synchronously.
func openLogs(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := press(m, "l")
	if cmd == nil {
		t.Fatal("l should start the logs view")
	}
	lv, ok := m.top().(*logsView)
	if !ok {
		t.Fatalf("top = %T", m.top())
	}
	mm, _ := m.Update(cmd()) // containersMsg -> restart -> wait cmd (abandoned)
	m = mm.(Model)
	if lv.streams != 2 || len(fl(m).calls) != 2 {
		t.Fatalf("expected two streams, got %d (%v)", lv.streams, fl(m).calls)
	}
	return m
}

// deliver pushes events into the merged channel the view is waiting on and
// runs one wait round.
func deliver(m Model, events ...k8s.LogEvent) Model {
	lv := m.top().(*logsView)
	mm, _ := m.Update(logLinesMsg{id: lv.id, events: events})
	return mm.(Model)
}

func TestLogsOpenMergeAndToggles(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "down") // b
	m = openLogs(t, m)
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "test-ctx › default › pods › b › logs") {
		t.Errorf("crumbs:\n%s", out)
	}
	// Lines from two containers arrive out of order; merged view sorts by time.
	m = deliver(m,
		k8s.LogEvent{Line: k8s.LogLine{Container: "sidecar", Time: ts(2), Text: "proxy ready"}},
		k8s.LogEvent{Line: k8s.LogLine{Container: "app", Time: ts(1), Text: "GET /healthz 200"}},
		k8s.LogEvent{Line: k8s.LogLine{Container: "app", Time: ts(3), Text: "GET /users 500"}},
	)
	out = stripANSI(m.View().Content)
	i1, i2, i3 := strings.Index(out, "GET /healthz"), strings.Index(out, "proxy ready"), strings.Index(out, "GET /users")
	if !(i1 < i2 && i2 < i3) || i1 < 0 {
		t.Errorf("lines not merged by time:\n%s", out)
	}
	if !strings.Contains(out, "app     GET /healthz") || !strings.Contains(out, "sidecar proxy ready") {
		t.Errorf("container prefix missing in merged view:\n%s", out)
	}
	if !strings.Contains(out, "3 lines") || !strings.Contains(out, "following") {
		t.Errorf("status missing:\n%s", out)
	}
	if strings.Contains(out, "12:00:01") {
		t.Error("timestamps should be hidden by default")
	}
	m, _ = press(m, "t")
	if !strings.Contains(stripANSI(m.View().Content), ts(1).Local().Format("15:04:05")) {
		t.Error("t should show timestamps")
	}
	// Regex filter, case-insensitive, with count.
	m, _ = press(m, "/")
	m = typeStr(m, "get.*500")
	out = stripANSI(m.View().Content)
	if strings.Contains(out, "healthz") || !strings.Contains(out, "GET /users 500") || !strings.Contains(out, "1 of 3 lines") {
		t.Errorf("regex filter wrong:\n%s", out)
	}
	m = typeStr(m, "(")
	if !strings.Contains(stripANSI(m.View().Content), "error parsing regexp") {
		t.Error("invalid regex should show an error")
	}
	m, _ = press(m, "esc")
	if m.top().(*logsView).re != nil {
		t.Error("esc while typing should clear the filter")
	}
	// Wrap toggles and the view still renders at height.
	m, _ = press(m, "w")
	if lines := strings.Count(m.View().Content, "\n") + 1; lines != 24 {
		t.Errorf("view has %d lines, want 24", lines)
	}
}

func TestLogsContainerAndSincePickers(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m = openLogs(t, m)
	m, _ = press(m, "c")
	m = typeStr(m, "sidecar")
	m, _ = press(m, "enter")
	lv := m.top().(*logsView)
	if lv.selected != "sidecar" || lv.streams != 1 || !strings.HasSuffix(fl(m).calls[len(fl(m).calls)-1], "/sidecar since=0s tail=1000") {
		t.Errorf("container switch: selected=%q streams=%d calls=%v", lv.selected, lv.streams, fl(m).calls)
	}
	if !strings.Contains(stripANSI(m.View().Content), "logs: sidecar") {
		t.Error("crumb should name the container")
	}
	m, _ = press(m, "s")
	m = typeStr(m, "15m")
	m, _ = press(m, "enter")
	if lv.opts.Since != 15*time.Minute || !strings.HasSuffix(fl(m).calls[len(fl(m).calls)-1], "since=15m0s tail=0") {
		t.Errorf("since: %v calls=%v", lv.opts.Since, fl(m).calls)
	}
	if !strings.Contains(stripANSI(m.View().Content), "since 15m") {
		t.Error("status should show the window")
	}
}

func TestLogsFollowPausesOnScroll(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m = openLogs(t, m)
	var events []k8s.LogEvent
	for i := 0; i < 60; i++ {
		events = append(events, k8s.LogEvent{Line: k8s.LogLine{Container: "app", Time: ts(i), Text: fmt.Sprintf("line %02d", i)}})
	}
	m = deliver(m, events...)
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "line 59") || strings.Contains(out, "line 00") {
		t.Errorf("should follow to the end:\n%s", out)
	}
	m, _ = press(m, "k")
	if m.top().(*logsView).follow || !strings.Contains(stripANSI(m.View().Content), "paused") {
		t.Error("scrolling up should pause follow")
	}
	m = deliver(m, k8s.LogEvent{Line: k8s.LogLine{Container: "app", Time: ts(60), Text: "line 60"}})
	if strings.Contains(stripANSI(m.View().Content), "line 60") {
		t.Error("paused view must not jump to new lines")
	}
	m, _ = press(m, "f")
	if !strings.Contains(stripANSI(m.View().Content), "line 60") {
		t.Error("f should resume follow at the end")
	}
	// Stream end is marked.
	m = deliver(m, k8s.LogEvent{Ended: true}, k8s.LogEvent{Ended: true})
	if !strings.Contains(stripANSI(m.View().Content), "stream ended") || !strings.Contains(stripANSI(m.View().Content), "ended") {
		t.Error("ended streams should be shown")
	}
}

func TestLogsSaveAndBack(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m = openLogs(t, m)
	m = deliver(m,
		k8s.LogEvent{Line: k8s.LogLine{Container: "app", Time: ts(1), Text: "hello"}},
		k8s.LogEvent{Line: k8s.LogLine{Container: "sidecar", Time: ts(2), Text: "world"}},
	)
	m, _ = press(m, "S")
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "saved ") {
		t.Fatalf("save notice missing:\n%s", out)
	}
	files, _ := os.ReadDir(m.saveDir)
	if len(files) != 1 || !strings.HasPrefix(files[0].Name(), "a-") {
		t.Fatalf("saved files = %v", files)
	}
	b, _ := os.ReadFile(m.saveDir + "/" + files[0].Name())
	if !strings.Contains(string(b), "[app] hello") || !strings.Contains(string(b), "[sidecar] world") || !strings.Contains(string(b), "2026-09-14T12:00:01Z") {
		t.Errorf("saved content:\n%s", b)
	}
	m, _ = press(m, "esc")
	if _, ok := m.top().(*resourceView); !ok {
		t.Error("esc should return to the table")
	}
	if m.top().(*resourceView).selectedKey() != "1" {
		t.Error("selection should be preserved")
	}
}

func TestLogsOnNonPodIsANotice(t *testing.T) {
	m := onDeployments(t)
	m, cmd := press(m, "l")
	if cmd == nil || len(m.stack) != 2 {
		t.Fatal("l on a deployment should not push a view")
	}
	if !strings.Contains(stripANSI(m.View().Content), "logs open from a pod") {
		t.Error("notice missing")
	}
}

func fe(m Model) *fakeExecer { return m.deps.exec.(*fakeExecer) }

// runShellCmd executes the batch a shell start returns, feeding the first
// output and the user probe back into the model.
func runShellCmd(m *Model, cmd tea.Cmd) {
	execCmdInto(m, cmd)
}

// pumpOnce runs the shell's wait command synchronously and feeds its
// message into the model. It fails the test if nothing arrives, rather than
// leaving a goroutine behind that would steal the next output.
func pumpOnce(t *testing.T, m *Model) {
	t.Helper()
	sv := m.top().(*shellView)
	if sv.done {
		return
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- sv.wait()() }()
	select {
	case msg := <-got:
		mm, _ := m.Update(msg)
		*m = mm.(Model)
	case <-time.After(3 * time.Second):
		t.Fatal("no shell output or exit arrived")
	}
}

// pumpUntilDone pumps until the session reports exit.
func pumpUntilDone(t *testing.T, m *Model) {
	t.Helper()
	for i := 0; i < 10 && !m.top().(*shellView).done; i++ {
		pumpOnce(t, m)
	}
}

// pumpUntilExit pumps the shell's wait loop until the shell view is gone
// (clean exit auto-pops) or reports done (error keeps it), running any
// command the exit produces.
func pumpUntilExit(t *testing.T, m *Model) {
	t.Helper()
	for i := 0; i < 12; i++ {
		sv, ok := m.top().(*shellView)
		if !ok {
			return // popped back
		}
		got := make(chan tea.Msg, 1)
		go func() { got <- sv.wait()() }()
		select {
		case msg := <-got:
			mm, cmd := m.Update(msg)
			*m = mm.(Model)
			execCmdInto(m, cmd)
		case <-time.After(3 * time.Second):
			t.Fatal("no shell output or exit arrived")
		}
		if _, still := m.top().(*shellView); !still {
			return
		}
	}
}

// shellText is the emulator screen, ANSI stripped and trailing space trimmed.
func shellText(m Model) string {
	sv := m.top().(*shellView)
	lines := strings.Split(stripANSI(sv.em.Render()), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func TestShellPicksContainerThenEmbeds(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "down") // b, which has app and sidecar
	m, cmd := press(m, "x")
	if cmd == nil {
		t.Fatal("x should fetch containers")
	}
	mm, cmd := m.Update(cmd())
	m = mm.(Model)
	if !m.palette.open || m.execTarget == nil {
		t.Fatal("two containers should open a picker")
	}
	m = typeStr(m, "side")
	m, cmd = press(m, "enter")
	sv, ok := m.top().(*shellView)
	if !ok || sv.container != "sidecar" {
		t.Fatalf("top = %T", m.top())
	}
	runShellCmd(&m, cmd)
	if len(fe(m).calls) != 1 || fe(m).calls[0] != "default/b/sidecar" {
		t.Errorf("exec calls = %v", fe(m).calls)
	}
	out := stripANSI(m.View().Content)
	for _, want := range []string{"test-ctx › default › pods › b › shell: sidecar", "user root", "default/b", "$ "} {
		if !strings.Contains(out, want) {
			t.Errorf("shell view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(sv.hint(), "ctrl+] closes it") {
		t.Errorf("hint = %q", sv.hint())
	}
	if !strings.Contains(out, "/ ___|") {
		t.Error("header should stay visible around the shell")
	}
	if m.View().Cursor == nil {
		t.Error("shell should place the cursor")
	}
	// Keys go to the shell and echo back through the emulator.
	m, _ = press(m, "l")
	pumpOnce(t, &m)
	m, _ = press(m, "s")
	pumpOnce(t, &m)
	if got := shellText(m); got != "$ ls" {
		t.Errorf("screen = %q", got)
	}
	// q does not quit while the shell runs.
	m, _ = press(m, "q")
	pumpOnce(t, &m)
	if len(m.stack) != 2 || m.top().(*shellView).done {
		t.Fatal("q must go to the shell, not quit")
	}
	// exit ends the session; the view stays until esc.
	for _, r := range "\x7f\x7f\x7fexit" {
		if r == '\x7f' {
			mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		} else {
			mm, _ = m.Update(kp(string(r)))
		}
		m = mm.(Model)
		pumpOnce(t, &m)
	}
	m, _ = press(m, "enter")
	pumpUntilExit(t, &m)
	if _, ok := m.top().(*resourceView); !ok {
		t.Fatalf("clean exit should auto-return to the table, top = %T", m.top())
	}
	out = stripANSI(m.View().Content)
	if !strings.Contains(out, "shell closed: default/b [sidecar]") {
		t.Errorf("close notice missing:\n%s", out)
	}
	if rv(m).selectedKey() != "2" {
		t.Error("selection should be preserved on return")
	}
}

func TestShellCloseKey(t *testing.T) {
	m, _, fg := newTestFull(t)
	fg.obj.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "only"}}}
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "x")
	mm, cmd := m.Update(cmd())
	m = mm.(Model)
	if m.palette.open {
		t.Fatal("one container should open the shell directly")
	}
	runShellCmd(&m, cmd)
	m, _ = press(m, "ctrl+]")
	pumpUntilExit(t, &m)
	if _, ok := m.top().(*resourceView); !ok {
		t.Errorf("ctrl+] should close the shell and return to the table, top = %T", m.top())
	}
}

func TestShellPickerEscCancels(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "x")
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	m, _ = press(m, "esc")
	if m.palette.open || m.execTarget != nil || len(fe(m).calls) != 0 {
		t.Error("esc should cancel the shell picker")
	}
}

func TestShellOnNonPodIsANotice(t *testing.T) {
	m := onDeployments(t)
	m, _ = press(m, "x")
	if !strings.Contains(stripANSI(m.View().Content), "shell opens from a pod") || len(fe(m).calls) != 0 {
		t.Error("x on a deployment should only notify")
	}
}

func TestShellErrorShown(t *testing.T) {
	m, _, fg := newTestFull(t)
	fg.obj.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "only"}}}
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "x")
	mm, cmd := m.Update(cmd())
	m = mm.(Model)
	runShellCmd(&m, cmd)
	sv := m.top().(*shellView)
	mm, cmd = m.Update(shellExitMsg{id: sv.id, err: errors.New("executable file not found")})
	m = mm.(Model)
	if cmd != nil {
		t.Error("a real error must not auto-pop the shell")
	}
	if _, ok := m.top().(*shellView); !ok {
		t.Fatal("error should keep the shell view visible")
	}
	if !strings.Contains(stripANSI(m.View().Content), "executable file not found") {
		t.Error("exec error should show in the body")
	}
	m, _ = press(m, "esc")
	if _, ok := m.top().(*resourceView); !ok {
		t.Error("esc after an error should return to the table")
	}
}
