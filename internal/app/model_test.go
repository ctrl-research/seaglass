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

func (f *fakeStreamer) Stream(ctx context.Context, res k8s.Resource, ns, fieldSelector, labelSelector string) <-chan k8s.Update {
	call := res.Name() + "/" + ns
	if fieldSelector != "" {
		call += "?" + fieldSelector
	}
	if labelSelector != "" {
		call += "#" + labelSelector
	}
	f.calls = append(f.calls, call)
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

// fakeEditor serves an object to edit and records updates.
type fakeEditor struct {
	obj     *unstructured.Unstructured
	updates []string // "res/ns/name: <yaml>"
	err     error
}

func (f *fakeEditor) Get(_ context.Context, res k8s.Resource, ns, name string) (*unstructured.Unstructured, error) {
	if f.obj != nil {
		return f.obj, nil
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": ns, "resourceVersion": "42"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "c", "image": "nginx:1.0"}}},
	}}, nil
}

func (f *fakeEditor) Update(_ context.Context, res k8s.Resource, ns, name string, y []byte) (*unstructured.Unstructured, error) {
	f.updates = append(f.updates, res.Name()+"/"+ns+"/"+name+": "+string(y))
	if f.err != nil {
		return nil, f.err
	}
	return &unstructured.Unstructured{}, nil
}

// fakeForwarder returns a canned PortForward or an error.
type fakeForwarder struct {
	calls []string // "ns/pod:remote"
	err   error
	local uint16
}

func (f *fakeForwarder) ForwardPort(ns, pod string, local, remote uint16) (*k8s.PortForward, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s/%s:%d", ns, pod, remote))
	if f.err != nil {
		return nil, f.err
	}
	lp := f.local
	if lp == 0 {
		lp = 30000 + remote
	}
	return k8s.NewTestForward(ns, pod, lp, remote), nil
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
		editer:    &fakeEditor{},
		forwarder: &fakeForwarder{},
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

func onDeploymentsFull(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	return feed(m, k8s.Update{Snapshot: deploySnap(), Status: k8s.StatusLive})
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
	if i1 < 0 || i1 >= i2 || i2 >= i3 {
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
	mm, _ := m.Update(cmd())
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

func schemaGVRapp(group, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: "v1", Resource: resource}
}

func fed(m Model) *fakeEditor { return m.deps.edit.(*fakeEditor) }

// prep runs prepareEdit for the selected row and returns the temp path and
// original content the editor would see.
func prep(t *testing.T, m Model) (Model, editPrepMsg) {
	t.Helper()
	m, cmd := press(m, "e")
	if cmd == nil {
		t.Fatal("e should prepare an edit")
	}
	msg, ok := cmd().(editPrepMsg)
	if !ok {
		t.Fatalf("expected editPrepMsg, got %T", cmd())
	}
	return m, msg
}

func TestEditNoChangeAborts(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, prepMsg := prep(t, m)
	if prepMsg.err != nil {
		t.Fatal(prepMsg.err)
	}
	// The banner explains what is being edited and the body is the object.
	if !strings.Contains(prepMsg.original, "# Editing Pod default/a.") || !strings.Contains(prepMsg.original, "image: nginx:1.0") {
		t.Errorf("edit buffer:\n%s", prepMsg.original)
	}
	if strings.Contains(prepMsg.original, "status:") || strings.Contains(prepMsg.original, "managedFields") {
		t.Error("status and managedFields should be stripped for editing")
	}
	// Editor exits with the file unchanged -> abort, no update.
	res := applyEdit(fed(m), editorDoneMsg{tgt: target{res: k8s.Pods, namespace: "default", name: "a"}, path: prepMsg.path, original: prepMsg.original})().(editResultMsg)
	if res.err != nil || res.summary != "edit aborted, no change" {
		t.Errorf("unchanged edit = %+v", res)
	}
	if len(fed(m).updates) != 0 {
		t.Error("no update should be sent for an unchanged edit")
	}
	if _, err := osStat(prepMsg.path); err == nil {
		t.Error("temp file should be removed")
	}
}

func TestEditAppliesChange(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, prepMsg := prep(t, m)
	// Simulate the user changing the image, then the editor exiting.
	edited := strings.Replace(prepMsg.original, "nginx:1.0", "nginx:2.0", 1)
	if err := osWrite(prepMsg.path, edited); err != nil {
		t.Fatal(err)
	}
	done := editorDoneMsg{tgt: target{res: k8s.Pods, namespace: "default", name: "a"}, path: prepMsg.path, original: prepMsg.original}
	res := applyEdit(fed(m), done)().(editResultMsg)
	if res.err != nil {
		t.Fatalf("apply failed: %v", res.err)
	}
	if len(fed(m).updates) != 1 || !strings.Contains(fed(m).updates[0], "nginx:2.0") {
		t.Errorf("updates = %v", fed(m).updates)
	}
	if strings.Contains(fed(m).updates[0], "# Editing") {
		t.Error("banner should be stripped before applying")
	}
	// The result message shows a notice.
	mm, _ := m.Update(res)
	m = mm.(Model)
	if !strings.Contains(stripANSI(m.View().Content), "applied edit to Pod default/a") {
		t.Error("notice missing after edit")
	}
}

func TestEditConflictAndInvalidSurface(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"conflict", k8s.ErrEditConflict{Detail: "the object has been modified"}, "the object has been modified"},
		{"invalid", k8s.ErrEditInvalid{Detail: "spec.replicas: Invalid value"}, "Invalid value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTest(t)
			m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
			m, prepMsg := prep(t, m)
			fed(m).err = tc.err
			edited := strings.Replace(prepMsg.original, "nginx:1.0", "nginx:2.0", 1)
			_ = osWrite(prepMsg.path, edited)
			done := editorDoneMsg{tgt: target{res: k8s.Pods, namespace: "default", name: "a"}, path: prepMsg.path, original: prepMsg.original}
			res := applyEdit(fed(m), done)().(editResultMsg)
			mm, _ := m.Update(res)
			m = mm.(Model)
			if !strings.Contains(stripANSI(m.View().Content), tc.want) {
				t.Errorf("%s not surfaced:\n%s", tc.name, stripANSI(m.View().Content))
			}
		})
	}
}

func TestEditFromObjectView(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "enter") // detail view
	m = openObject(m)
	_, cmd := press(m, "e")
	if cmd == nil {
		t.Fatal("e should prepare an edit from the object view")
	}
	if _, ok := cmd().(editPrepMsg); !ok {
		t.Fatalf("expected editPrepMsg, got %T", cmd())
	}
}

func osStat(p string) (os.FileInfo, error) { return os.Stat(p) }
func osWrite(p, content string) error      { return os.WriteFile(p, []byte(content), 0o600) }

func ff(m Model) *fakeForwarder { return m.deps.fwd.(*fakeForwarder) }

// podPortsObj sets the fake getter's pod to expose the given ports.
func setPodPorts(m Model, ports ...int) {
	fg := m.deps.get.(*fakeGetter)
	var ps []any
	for _, p := range ports {
		ps = append(ps, map[string]any{"containerPort": int64(p)})
	}
	fg.obj.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "app", "ports": ps}}}
}

func TestForwardSinglePortStartsDirectly(t *testing.T) {
	m, _, _ := newTestFull(t)
	setPodPorts(m, 8080)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "F")
	if cmd == nil {
		t.Fatal("F should fetch pod ports")
	}
	mm, cmd := m.Update(cmd()) // fwdContainersMsg -> startForward cmd
	m = mm.(Model)
	if m.palette.open || m.prompt.open {
		t.Fatal("a single port should forward directly")
	}
	mm, _ = m.Update(cmd()) // forwardStartedMsg
	m = mm.(Model)
	if ff(m).calls[0] != "default/a:8080" {
		t.Errorf("forward call = %v", ff(m).calls)
	}
	if m.forwards.count() != 1 {
		t.Fatalf("expected one active forward, got %d", m.forwards.count())
	}
	if !strings.Contains(stripANSI(m.View().Content), "forwarding 127.0.0.1:38080 → default/a:8080") {
		t.Errorf("notice missing:\n%s", stripANSI(m.View().Content))
	}
	// Once the transient notice clears, the persistent ⇄ indicator shows.
	mm, _ = m.Update(clearNoticeMsg{seq: m.noticeSeq})
	m = mm.(Model)
	if !strings.Contains(stripANSI(m.View().Content), "⇄1") {
		t.Errorf("status should show the forward indicator:\n%s", stripANSI(m.View().Content))
	}
}

func TestForwardMultiPortPicker(t *testing.T) {
	m, _, _ := newTestFull(t)
	setPodPorts(m, 8080, 9090)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "F")
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if !m.palette.open || m.fwdTarget == nil {
		t.Fatal("multiple ports should open a picker")
	}
	m = typeStr(m, "9090")
	m, cmd = press(m, "enter")
	mm, _ = m.Update(cmd())
	m = mm.(Model)
	if ff(m).calls[0] != "default/a:9090" {
		t.Errorf("forward call = %v", ff(m).calls)
	}
}

func TestForwardNoPortsPrompts(t *testing.T) {
	m, _, _ := newTestFull(t)
	setPodPorts(m) // none
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "F")
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if !m.prompt.open || m.fwdTarget == nil {
		t.Fatal("a pod with no declared ports should prompt for one")
	}
	m = typeStr(m, "5432")
	m, cmd = press(m, "enter")
	if cmd == nil {
		t.Fatal("submitting the port should start a forward")
	}
	execCmdInto(&m, cmd)
	if len(ff(m).calls) != 1 || ff(m).calls[0] != "default/a:5432" {
		t.Errorf("forward call = %v", ff(m).calls)
	}
}

func TestForwardsPanelCancel(t *testing.T) {
	m, _, _ := newTestFull(t)
	setPodPorts(m, 8080)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "F")
	mm, cmd := m.Update(cmd())
	m = mm.(Model)
	mm, _ = m.Update(cmd()) // started
	m = mm.(Model)
	// Open the forwards panel via the palette.
	m, _ = press(m, ":")
	m = typeStr(m, "forwards")
	m, _ = press(m, "enter")
	fv, ok := m.top().(*forwardsView)
	if !ok {
		t.Fatalf("palette should open the forwards panel, got %T", m.top())
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "127.0.0.1:38080 → default/a:8080") {
		t.Errorf("panel should list the forward:\n%s", out)
	}
	_ = fv
	// ctrl+d now asks for confirmation before cancelling.
	m, _ = press(m, "ctrl+d")
	if m.confirm == nil {
		t.Fatal("ctrl+d should ask before cancelling a forward")
	}
	if !strings.Contains(stripANSI(m.View().Content), "cancel port-forward?") {
		t.Errorf("confirm text:\n%s", stripANSI(m.View().Content))
	}
	// n aborts, the forward stays.
	m, _ = press(m, "n")
	if m.forwards.count() != 1 {
		t.Fatal("n should keep the forward")
	}
	// y cancels it.
	m, _ = press(m, "ctrl+d")
	m, cmd = press(m, "y")
	m = runResult(m, cmd)
	if m.forwards.count() != 0 {
		t.Error("y should cancel the selected forward")
	}
	if !strings.Contains(stripANSI(m.View().Content), "stopped forward") {
		t.Error("cancel notice missing")
	}
	m, _ = press(m, "esc")
	if _, ok := m.top().(*resourceView); !ok {
		t.Error("esc should leave the panel")
	}
}

func TestForwardDeathRemovesIt(t *testing.T) {
	m, _, _ := newTestFull(t)
	setPodPorts(m, 8080)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "F")
	mm, cmd := m.Update(cmd())
	m = mm.(Model)
	mm, _ = m.Update(cmd())
	m = mm.(Model)
	id := m.forwards.list[0].id
	mm, _ = m.Update(forwardDiedMsg{id: id, err: errors.New("pod deleted")})
	m = mm.(Model)
	if m.forwards.count() != 0 {
		t.Error("a died forward should be removed")
	}
	if !strings.Contains(stripANSI(m.View().Content), "stopped") {
		t.Logf("view: %s", stripANSI(m.View().Content))
	}
}

func TestForwardOnNonPodIsANotice(t *testing.T) {
	m := onDeployments(t)
	m, _ = press(m, "F")
	if !strings.Contains(stripANSI(m.View().Content), "port-forward opens from a pod") {
		t.Error("F on a deployment should notify")
	}
}

func TestCopyPicker(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "c")
	if !m.palette.open {
		t.Fatal("c should open the copy picker")
	}
	// Default namespace is "default"; the picker offers name, namespace, etc.
	labels := map[string]bool{}
	for _, it := range m.palette.items {
		labels[it.Label] = true
	}
	for _, want := range []string{"name", "namespace", "namespace/name", "kubectl get", "kubectl describe"} {
		if !labels[want] {
			t.Errorf("copy picker missing %q", want)
		}
	}
	// Choosing "kubectl get" copies a context-scoped command.
	m = typeStr(m, "kubectl get")
	it, ok := m.palette.selected()
	if !ok || it.Kind != itemCopy {
		t.Fatalf("selected %+v", it)
	}
	m, cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("enter should copy (SetClipboard + notice)")
	}
	want := "kubectl --context test-ctx -n default get pods/a"
	if it.Text != want {
		t.Errorf("copy text = %q, want %q", it.Text, want)
	}
	// The status bar truncates; check the full notice on the model.
	if m.notice != "copied: "+want {
		t.Errorf("notice = %q", m.notice)
	}
}

func TestCopyNameOnly(t *testing.T) {
	items := copyItems(k8s.Pods, "", "web-1", "")
	// Cluster-scoped (no namespace) copy: no namespace entry, no -n flag.
	for _, it := range items {
		if it.Label == "namespace" {
			t.Error("no namespace entry when namespace is empty")
		}
		if it.Kind == itemCopy && it.Label == "kubectl get" && strings.Contains(it.Text, " -n ") {
			t.Errorf("cluster-scoped command should have no -n: %q", it.Text)
		}
	}
	if items[0].Text != "web-1" {
		t.Errorf("first copy item should be the name, got %q", items[0].Text)
	}
}

// drainRollout drives a rollout view to completion: it expands the start
// batch, runs poll commands, feeds their status messages back, and
// simulates each scheduled tick by polling again.
func drainRollout(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	// Collect leaf commands from a (possibly batched) command.
	var leaves func(tea.Cmd) []tea.Cmd
	leaves = func(c tea.Cmd) []tea.Cmd {
		if c == nil {
			return nil
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Cmd
			for _, cc := range b {
				out = append(out, leaves(cc)...)
			}
			return out
		}
		// Feed non-command messages into the model.
		mm, next := m.Update(msg)
		*m = mm.(Model)
		return leaves(next)
	}
	for i := 0; i < 30; i++ {
		rv, ok := m.top().(*rolloutView)
		if !ok || rv.done {
			return
		}
		leaves(rv.poll())
	}
}

func TestRolloutStatusView(t *testing.T) {
	m, _, fg := newTestFull(t)
	// Serve a completed deployment.
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "default", "generation": int64(2)},
		"spec":     map[string]any{"replicas": int64(3)},
		"status":   map[string]any{"observedGeneration": int64(2), "updatedReplicas": int64(3), "replicas": int64(3), "availableReplicas": int64(3)},
	}}
	m = onDeploymentsFull(t, m)
	m, cmd := press(m, "R")
	if cmd == nil {
		t.Fatal("R should open the rollout view")
	}
	rv, ok := m.top().(*rolloutView)
	if !ok {
		t.Fatalf("top = %T", m.top())
	}
	out := stripANSI(m.View().Content)
	if !strings.Contains(out, "test-ctx › default › deployments › web › rollout") {
		t.Errorf("crumbs:\n%s", out)
	}
	// Drive the poll: it should complete.
	drainRollout(t, &m, cmd)
	if !m.top().(*rolloutView).done {
		t.Fatal("rollout should complete")
	}
	out = stripANSI(m.View().Content)
	if !strings.Contains(out, "successfully rolled out") || !strings.Contains(out, "rollout complete") {
		t.Errorf("completion missing:\n%s", out)
	}
	if !strings.Contains(out, "complete") {
		t.Error("status should say complete")
	}
	_ = rv
	// esc returns to the deployments table.
	m, _ = press(m, "esc")
	if _, ok := m.top().(*resourceView); !ok {
		t.Error("esc should return to the table")
	}
}

func TestRestartFollowsRollout(t *testing.T) {
	m, _, fg := newTestFull(t)
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "default", "generation": int64(1)},
		"spec":     map[string]any{"replicas": int64(1)},
		"status":   map[string]any{"observedGeneration": int64(1), "updatedReplicas": int64(1), "replicas": int64(1), "availableReplicas": int64(1)},
	}}
	m = onDeploymentsFull(t, m)
	m, _ = press(m, "r") // restart -> confirm
	if m.confirm == nil {
		t.Fatal("restart should confirm")
	}
	m, cmd := press(m, "y")
	// runAction returns actionResultMsg with follow=true.
	msg := cmd().(actionResultMsg)
	if !msg.follow {
		t.Fatal("restart result should request follow")
	}
	mm, cmd := m.Update(msg)
	m = mm.(Model)
	if _, ok := m.top().(*rolloutView); !ok {
		t.Fatalf("restart should open a rollout view, got %T", m.top())
	}
	drainRollout(t, &m, cmd)
	if !strings.Contains(stripANSI(m.View().Content), "rolled out") {
		t.Errorf("rollout not followed:\n%s", stripANSI(m.View().Content))
	}
}

func TestPodsNotRolloutable(t *testing.T) {
	m, _, _ := newTestFull(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "R")
	if cmd != nil || len(m.stack) != 1 {
		t.Error("R on pods should do nothing")
	}
}

func eventsSnap() k8s.Snapshot {
	return k8s.Snapshot{
		Columns: []k8s.Column{{Name: "Last Seen"}, {Name: "Type"}, {Name: "Reason"}, {Name: "Object"}, {Name: "Message"}},
		Rows: []k8s.Row{
			{Name: "e1", Namespace: "default", UID: "e1", Cells: []string{"2m", "Normal", "Scheduled", "pod/a", "assigned"}},
			{Name: "e2", Namespace: "default", UID: "e2", Cells: []string{"30s", "Warning", "BackOff", "pod/a", "Back-off restarting"}},
		},
	}
}

func TestEventsForObject(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "E")
	if cmd == nil {
		t.Fatal("E should open events")
	}
	rv, ok := m.top().(*resourceView)
	if !ok || rv.res.GVR != k8s.Events.GVR {
		t.Fatalf("top should be an events view, got %T", m.top())
	}
	// Streamed with an involvedObject selector for the selected pod (uid "1").
	fs := m.deps.stream.(*fakeStreamer)
	last := fs.calls[len(fs.calls)-1]
	if !strings.Contains(last, "events/default?involvedObject.uid=1") {
		t.Errorf("events stream call = %q", last)
	}
	if !strings.Contains(stripANSI(m.View().Content), "› events: a") {
		t.Errorf("crumb should name the object:\n%s", stripANSI(m.View().Content))
	}
	// Feed events; the warning row is colored (contains an ANSI code).
	m = feed(m, k8s.Update{Snapshot: eventsSnap(), Status: k8s.StatusLive})
	rv = m.top().(*resourceView)
	if rv.warnCol != 1 {
		t.Errorf("warn column = %d, want 1 (Type)", rv.warnCol)
	}
	raw := m.View().Content
	if !strings.Contains(stripANSI(raw), "Back-off restarting") {
		t.Error("warning event row missing")
	}
	// esc returns to the pods table.
	m, _ = press(m, "esc")
	if _, ok := m.top().(*resourceView); !ok || m.top().(*resourceView).res.GVR != k8s.Pods.GVR {
		t.Error("esc should return to pods")
	}
}

func TestClusterEventsAction(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, ":")
	m = typeStr(m, "events")
	it, ok := m.palette.selected()
	if !ok || it.Name != actionEvents {
		t.Fatalf("events action not first, got %+v", it)
	}
	m, _ = press(m, "enter")
	rv, ok := m.top().(*resourceView)
	if !ok || rv.res.GVR != k8s.Events.GVR || rv.fieldSelector != "" {
		t.Fatalf("cluster events view wrong: %T sel=%q", m.top(), rv.fieldSelector)
	}
	fs := m.deps.stream.(*fakeStreamer)
	if last := fs.calls[len(fs.calls)-1]; last != "events/default" {
		t.Errorf("cluster events stream = %q", last)
	}
}

// podWithOwner sets the fake getter's object to a pod owned by a ReplicaSet.
func podWithOwner(m Model, name, ownerKind, ownerName, ownerAPI string) {
	tru := true
	fg := m.deps.get.(*fakeGetter)
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": "default", "uid": "1",
			"ownerReferences": []any{map[string]any{
				"apiVersion": ownerAPI, "kind": ownerKind, "name": ownerName, "controller": tru,
			}},
		},
	}}
}

func TestOwnerJumpFromTable(t *testing.T) {
	m, _ := newTest(t)
	// Discovery knows replicasets.
	mm, _ := m.Update(resourcesMsg{resources: []k8s.Resource{
		k8s.Pods,
		{GVR: schemaGVRapp("apps", "replicasets"), Kind: "ReplicaSet", Namespaced: true},
		{GVR: schemaGVRapp("apps", "deployments"), Kind: "Deployment", Namespaced: true},
	}})
	m = mm.(Model)
	podWithOwner(m, "a", "ReplicaSet", "web-rs", "apps/v1")
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "o")
	if cmd == nil {
		t.Fatal("o should fetch and jump")
	}
	mm, _ = m.Update(cmd()) // ownerJumpMsg -> handleOwnerJump
	m = mm.(Model)
	ov, ok := m.top().(*objectView)
	if !ok || ov.res.Kind != "ReplicaSet" || ov.name != "web-rs" {
		t.Fatalf("owner jump target = %T %+v", m.top(), m.top())
	}
	if !strings.Contains(stripANSI(m.View().Content), "› replicasets › web-rs") {
		t.Errorf("crumbs:\n%s", stripANSI(m.View().Content))
	}
}

func TestOwnerJumpNoOwner(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	// fakeGetter default object has no ownerReferences.
	m, cmd := press(m, "o")
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if !strings.Contains(stripANSI(m.View().Content), "no owner reference") {
		t.Errorf("expected a no-owner notice:\n%s", stripANSI(m.View().Content))
	}
	if _, ok := m.top().(*resourceView); !ok {
		t.Error("should stay on the table when there is no owner")
	}
}

func TestOwnerJumpUnknownKind(t *testing.T) {
	m, _ := newTest(t)
	// No discovery for the owner's kind.
	podWithOwner(m, "a", "CronJob", "nightly", "batch/v1")
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "o")
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if !strings.Contains(stripANSI(m.View().Content), "CronJob is not a known resource") {
		t.Errorf("expected an unknown-kind notice:\n%s", stripANSI(m.View().Content))
	}
}

func TestRelatedDeploymentToPods(t *testing.T) {
	m, _ := newTest(t)
	fg := m.deps.get.(*fakeGetter)
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "default"},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web", "tier": "front"}}},
	}}
	// Point the view at deployments.
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	m = feed(m, k8s.Update{Snapshot: deploySnap(), Status: k8s.StatusLive})
	m, cmd := press(m, "J")
	if cmd == nil {
		t.Fatal("J should compute related")
	}
	mm, _ := m.Update(cmd()) // relatedMsg -> single target -> navigate
	m = mm.(Model)
	rv, ok := m.top().(*resourceView)
	if !ok || rv.res.GVR != k8s.Pods.GVR || rv.labelSelector != "app=web,tier=front" {
		t.Fatalf("related pods view wrong: %T sel=%q", m.top(), rv.labelSelector)
	}
	fs := m.deps.stream.(*fakeStreamer)
	if last := fs.calls[len(fs.calls)-1]; last != "pods/default#app=web,tier=front" {
		t.Errorf("stream call = %q", last)
	}
	if !strings.Contains(stripANSI(m.View().Content), "› pods of web") {
		t.Errorf("crumb:\n%s", stripANSI(m.View().Content))
	}
}

func TestRelatedPodPicker(t *testing.T) {
	m, _ := newTest(t)
	// Node discovery for the node jump.
	mm, _ := m.Update(resourcesMsg{resources: []k8s.Resource{k8s.Pods, {GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Kind: "Node"}}})
	m = mm.(Model)
	fg := m.deps.get.(*fakeGetter)
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "a", "namespace": "default"},
		"spec":     map[string]any{"nodeName": "node-1"},
	}}
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "J")
	mm, _ = m.Update(cmd())
	m = mm.(Model)
	// A pod with only a node has one related target -> navigates directly.
	ov, ok := m.top().(*objectView)
	if !ok || ov.res.Kind != "Node" || ov.name != "node-1" {
		t.Fatalf("pod→node jump wrong: %T", m.top())
	}
}

func TestRelatedNodeToPods(t *testing.T) {
	m, _ := newTest(t)
	fg := m.deps.get.(*fakeGetter)
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]any{"name": "node-1"},
	}}
	// Simulate a nodes table by switching resource via choose.
	m.stack[0].(*resourceView).res = k8s.Resource{GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Kind: "Node"}
	m = feed(m, k8s.Update{Snapshot: k8s.Snapshot{Columns: []k8s.Column{{Name: "Name"}}, Rows: []k8s.Row{{Name: "node-1", Cells: []string{"node-1"}}}}, Status: k8s.StatusLive})
	m, cmd := press(m, "J")
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	rv, ok := m.top().(*resourceView)
	if !ok || rv.fieldSelector != "spec.nodeName=node-1" {
		t.Fatalf("node→pods wrong: %T sel=%q", m.top(), rv.fieldSelector)
	}
	fs := m.deps.stream.(*fakeStreamer)
	if last := fs.calls[len(fs.calls)-1]; last != "pods/?spec.nodeName=node-1" {
		t.Errorf("stream call = %q", last)
	}
}

func TestRelatedNone(t *testing.T) {
	m, _ := newTest(t)
	// Default pod object has no selector, no nodeName.
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, cmd := press(m, "J")
	mm, _ := m.Update(cmd())
	m = mm.(Model)
	if !strings.Contains(stripANSI(m.View().Content), "no related resources") {
		t.Errorf("expected no-related notice:\n%s", stripANSI(m.View().Content))
	}
}

func TestPodTableColorsStatusColumn(t *testing.T) {
	m, _ := newTest(t)
	// A snapshot with a Status column and a failing pod.
	sn := k8s.Snapshot{
		Columns: []k8s.Column{{Name: "Name"}, {Name: "Ready"}, {Name: "Status"}, {Name: "Age"}},
		Rows: []k8s.Row{
			{Name: "ok", UID: "1", Cells: []string{"ok", "1/1", "Running", "1h"}},
			{Name: "bad", UID: "2", Cells: []string{"bad", "0/1", "CrashLoopBackOff", "1h"}},
		},
	}
	m = feed(m, k8s.Update{Snapshot: sn, Status: k8s.StatusLive})
	rv := m.top().(*resourceView)
	if rv.statusCol != 2 {
		t.Fatalf("status column not resolved: %d", rv.statusCol)
	}
	// The rendered content colors the failing status (has an ANSI code that
	// the plain text lacks).
	raw := m.View().Content
	if !strings.Contains(raw, "CrashLoopBackOff") {
		t.Fatal("status text missing")
	}
	// Extract the styled cell: the error color 203 should wrap the status.
	if !strings.Contains(raw, "203") {
		t.Errorf("failing status should be colored (203):\n%q", raw)
	}
}

func TestResourceViewDecorationDefaults(t *testing.T) {
	v := newResourceView(1, k8s.Pods, "default")
	if v.warnCol != -1 || v.statusCol != -2 {
		t.Errorf("decoration sentinels wrong: warnCol=%d statusCol=%d", v.warnCol, v.statusCol)
	}
}

func newTestWithRules(t *testing.T, rs config.Ruleset) (Model, *fakeStreamer, *fakeGetter) {
	t.Helper()
	fs := &fakeStreamer{}
	fg := &fakeGetter{obj: &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system"},
		"status":   map[string]any{},
	}}}
	fluxRes := k8s.Resource{GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Kind: "Kustomization", Namespaced: true}
	m := New(Options{
		Client:    &k8s.Client{Context: "test-ctx", Namespace: "flux-system"},
		Namespace: "flux-system",
		Resource:  fluxRes,
		SaveDir:   t.TempDir(),
		streamer:  fs,
		getter:    fg,
		patcher:   &fakePatcher{},
		logger:    &fakeLogger{},
		execer:    &fakeExecer{},
		editer:    &fakeEditor{},
		forwarder: &fakeForwarder{},
		contexts:  []string{"test-ctx"},
		Ruleset:   rs,
	})
	rv(m).start(m.deps)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = mm.(Model)
	return m, fs, fg
}

func fluxRuleset(t *testing.T) config.Ruleset {
	t.Helper()
	presets, err := config.Presets()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range presets {
		if p.Name == "flux" {
			return p.Rules
		}
	}
	t.Fatal("flux preset missing")
	return config.Ruleset{}
}

func fluxSnap() k8s.Snapshot {
	return k8s.Snapshot{
		Columns: []k8s.Column{{Name: "Name"}, {Name: "Ready"}, {Name: "Status"}},
		Rows:    []k8s.Row{{Name: "apps", Namespace: "flux-system", UID: "k1", Cells: []string{"apps", "True", "Applied"}}},
	}
}

func TestConfigActionSuspendResume(t *testing.T) {
	m, _, _ := newTestWithRules(t, fluxRuleset(t))
	m = feed(m, k8s.Update{Snapshot: fluxSnap(), Status: k8s.StatusLive})
	// The palette offers the flux actions for the selected Kustomization.
	m, _ = press(m, ":")
	labels := map[string]bool{}
	for _, it := range m.palette.items {
		labels[it.Label] = true
	}
	for _, want := range []string{"reconcile", "suspend", "resume"} {
		if !labels[want] {
			t.Errorf("palette missing flux action %q", want)
		}
	}
	// Choose suspend: it confirms, then patches spec.suspend=true.
	m = typeStr(m, "suspend")
	m, _ = press(m, "enter")
	if m.confirm == nil {
		t.Fatal("suspend should confirm")
	}
	m, cmd := press(m, "y")
	m = runResult(m, cmd)
	fp := fp(m)
	if len(fp.patches) != 1 || !strings.Contains(fp.patches[0], `"suspend":true`) {
		t.Fatalf("suspend patch = %v", fp.patches)
	}
	if !strings.Contains(fp.patches[0], "kustomizations/flux-system/apps") {
		t.Errorf("patched wrong object: %s", fp.patches[0])
	}
}

func TestConfigActionResumePatches(t *testing.T) {
	m, _, _ := newTestWithRules(t, fluxRuleset(t))
	m = feed(m, k8s.Update{Snapshot: fluxSnap(), Status: k8s.StatusLive})
	m, _ = press(m, ":")
	m = typeStr(m, "resume")
	m, _ = press(m, "enter")
	// resume has no confirm in the preset; it runs directly.
	if m.confirm != nil {
		t.Fatal("resume should not confirm")
	}
	// The choose returned a cmd; run it and its result.
	// Re-open and choose to capture the cmd:
	m2, _, _ := newTestWithRules(t, fluxRuleset(t))
	m2 = feed(m2, k8s.Update{Snapshot: fluxSnap(), Status: k8s.StatusLive})
	m2, _ = press(m2, ":")
	m2 = typeStr(m2, "resume")
	m2, cmd := press(m2, "enter")
	if cmd == nil {
		t.Fatal("resume should run a command")
	}
	execCmdInto(&m2, cmd)
	fp := fp(m2)
	if len(fp.patches) != 1 || !strings.Contains(fp.patches[0], `"suspend":false`) {
		t.Fatalf("resume patch = %v", fp.patches)
	}
}

func TestWaitForActionFieldPredicate(t *testing.T) {
	// A wait on a status field that the fetched object already satisfies
	// returns immediately.
	fg := &fakeGetter{obj: &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system"},
		"status":   map[string]any{"lastHandledReconcileAt": "T1"},
	}}}
	d := deps{get: fg}
	tgt := target{res: k8s.Resource{GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Kind: "Kustomization", Namespaced: true}, namespace: "flux-system", name: "apps"}
	// Static equals "T1" so no template/now involved.
	w := &config.WaitRule{Field: "status.lastHandledReconcileAt", Equals: "T1", Timeout: config.Duration(2 * time.Second)}
	data := config.NewRenderData(fg.obj.Object, "", time.Now())
	if err := waitForAction(d, tgt, w, data); err != nil {
		t.Errorf("satisfied predicate should not error: %v", err)
	}
	// A predicate that never holds times out.
	w2 := &config.WaitRule{Field: "status.lastHandledReconcileAt", Equals: "NEVER", Timeout: config.Duration(1 * time.Second)}
	if err := waitForAction(d, tgt, w2, data); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("unsatisfied predicate should time out, got %v", err)
	}
}

func TestConfigJumpFieldRef(t *testing.T) {
	// A Kustomization with spec.sourceRef -> GitRepository, plus a jump rule.
	rs := config.Ruleset{Jumps: []config.JumpRule{
		{Name: "source", Match: config.Match{Group: "kustomize.toolkit.fluxcd.io"}, From: config.JumpFrom{Field: "spec.sourceRef"}},
	}}
	m, _, fg := newTestWithRules(t, rs)
	// Discovery includes GitRepository.
	mm, _ := m.Update(resourcesMsg{resources: []k8s.Resource{
		{GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Kind: "Kustomization", Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}, Kind: "GitRepository", Namespaced: true},
	}})
	m = mm.(Model)
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system"},
		"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"}},
	}}
	m = feed(m, k8s.Update{Snapshot: fluxSnap(), Status: k8s.StatusLive})
	m, cmd := press(m, "J")
	if cmd == nil {
		t.Fatal("J should compute related")
	}
	mm, _ = m.Update(cmd()) // single target -> navigate
	m = mm.(Model)
	ov, ok := m.top().(*objectView)
	if !ok || ov.res.Kind != "GitRepository" || ov.name != "flux-system" {
		t.Fatalf("source jump target = %T %+v", m.top(), m.top())
	}
}

func TestConfigJumpManagedByLabel(t *testing.T) {
	// A Deployment labeled by a Kustomization; managed-by jump opens it.
	rs := config.Ruleset{Jumps: []config.JumpRule{
		{Name: "managed by", Match: config.Match{}, From: config.JumpFrom{NameLabel: "kustomize.toolkit.fluxcd.io/name", NamespaceLabel: "kustomize.toolkit.fluxcd.io/namespace"}, To: config.Match{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}},
	}}
	m, _, fg := newTestWithRules(t, rs)
	mm, _ := m.Update(resourcesMsg{resources: []k8s.Resource{
		{GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Kind: "Kustomization", Namespaced: true},
	}})
	m = mm.(Model)
	fg.obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "apps",
			"labels": map[string]any{
				"kustomize.toolkit.fluxcd.io/name":      "apps",
				"kustomize.toolkit.fluxcd.io/namespace": "flux-system",
			}},
	}}
	m = feed(m, k8s.Update{Snapshot: fluxSnap(), Status: k8s.StatusLive})
	m, cmd := press(m, "J")
	mm, _ = m.Update(cmd())
	m = mm.(Model)
	ov, ok := m.top().(*objectView)
	if !ok || ov.res.Kind != "Kustomization" || ov.name != "apps" || ov.namespace != "flux-system" {
		t.Fatalf("managed-by jump wrong: %T %+v", m.top(), m.top())
	}
}
