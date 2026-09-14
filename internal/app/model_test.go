package app

import (
	"context"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

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

func newTest(t *testing.T) (Model, *fakeStreamer) {
	t.Helper()
	fs := &fakeStreamer{}
	m := New(Options{
		Client:    &k8s.Client{Context: "test-ctx", Namespace: "default"},
		Namespace: "default",
		Resource:  k8s.Pods,
		streamer:  fs,
		contexts:  []string{"test-ctx", "other-ctx"},
	})
	// Init returns a batch; run the stream start directly instead so tests
	// stay synchronous.
	m.top().start(fs)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = mm.(Model)
	mm, _ = m.Update(resourcesMsg{resources: []k8s.Resource{k8s.Pods, deployments, nodes, k8s.Namespaces}})
	m = mm.(Model)
	mm, _ = m.Update(namespacesMsg{names: []string{"default", "kube-system"}})
	return mm.(Model), fs
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

func key(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func press(m Model, keys ...string) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		var mm tea.Model
		mm, cmd = m.Update(key(k))
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
	mm, _ := m.Update(updateMsg{id: m.top().id, Update: u})
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
	if m.top().selectedKey() != "2" {
		t.Fatalf("selected %q after down, want b", m.top().selectedKey())
	}
	s := snap()
	s.Rows = append([]k8s.Row{{Name: "0", UID: "0", Cells: []string{"0", "Running", ""}}}, s.Rows...)
	m = feed(m, k8s.Update{Snapshot: s, Status: k8s.StatusLive})
	if m.top().selectedKey() != "2" {
		t.Errorf("selection moved to %q after insert above", m.top().selectedKey())
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
	if len(m.stack) != 2 || m.top().res.Name() != "deployments" {
		t.Fatalf("stack = %d, top = %s", len(m.stack), m.top().res.Name())
	}
	if got := fs.calls[len(fs.calls)-1]; got != "deployments/default" {
		t.Errorf("stream started for %q", got)
	}
	out := m.View().Content
	if !strings.Contains(out, "pods") || !strings.Contains(out, "deployments") {
		t.Errorf("breadcrumbs missing:\n%s", out)
	}

	m, _ = press(m, "esc")
	if len(m.stack) != 1 || m.top().res.Name() != "pods" {
		t.Fatalf("esc should pop back to pods, got %s", m.top().res.Name())
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
	if m.namespace != "kube-system" || len(m.stack) != 1 || m.top().res.Name() != "deployments" {
		t.Fatalf("ns=%s stack=%d top=%s", m.namespace, len(m.stack), m.top().res.Name())
	}
	if got := fs.calls[len(fs.calls)-1]; got != "deployments/kube-system" {
		t.Errorf("stream = %q", got)
	}
}

func TestStaleUpdateIgnored(t *testing.T) {
	m, _ := newTest(t)
	oldID := m.top().id
	m, _ = press(m, ":")
	m = typeStr(m, "deploy")
	m, _ = press(m, "enter")
	mm, _ := m.Update(updateMsg{id: oldID, Update: k8s.Update{Snapshot: snap(), Status: k8s.StatusLive}})
	m = mm.(Model)
	if len(m.top().snapshot.Rows) != 0 {
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
	if !m.top().typing {
		t.Fatal("/ should start the filter")
	}
	m = typeStr(m, "pend")
	if got := len(m.top().filtered); got != 1 || m.top().filtered[0].Name != "b" {
		t.Fatalf("filter 'pend' -> %d rows (%+v)", got, m.top().filtered)
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
	if m.top().filter.Value() != "pendq" {
		t.Errorf("filter value = %q", m.top().filter.Value())
	}
	if len(m.top().filtered) != 0 || !strings.Contains(stripANSI(m.View().Content), "0 of 3") {
		t.Error("no rows should match 'pendq'")
	}
	// Backspace, then enter keeps the filter applied without focus.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = mm.(Model)
	m, _ = press(m, "enter")
	if m.top().typing || len(m.top().filtered) != 1 {
		t.Fatalf("enter should keep filter: typing=%v rows=%d", m.top().typing, len(m.top().filtered))
	}
	if !strings.Contains(stripANSI(m.View().Content), "1 of 3 rows") {
		t.Error("status bar should show filtered count")
	}
	// Live update keeps the filter applied.
	s := snap()
	s.Rows = append(s.Rows, k8s.Row{Name: "d", UID: "4", Cells: []string{"d", "Pending", ""}})
	m = feed(m, k8s.Update{Snapshot: s, Status: k8s.StatusLive})
	if len(m.top().filtered) != 2 {
		t.Errorf("filter not reapplied on update: %d rows", len(m.top().filtered))
	}
	// esc clears the filter instead of popping.
	m, _ = press(m, "esc")
	if m.top().filterActive() || len(m.top().filtered) != 4 || len(m.stack) != 1 {
		t.Errorf("esc should clear the filter: active=%v rows=%d stack=%d", m.top().filterActive(), len(m.top().filtered), len(m.stack))
	}
}

func TestFilterEscWhileTypingClears(t *testing.T) {
	m, _ := newTest(t)
	m = feed(m, k8s.Update{Snapshot: snap(), Status: k8s.StatusLive})
	m, _ = press(m, "/")
	m = typeStr(m, "run")
	m, _ = press(m, "esc")
	if m.top().filterActive() || len(m.top().filtered) != 3 {
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
	if r, ok := m.top().selectedRow(); !ok || r.Name != "c" {
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
	if got := len(m.top().filtered); got != 1 || m.top().filtered[0].Name != "coredns-1" {
		t.Errorf("'core' should match only coredns, got %d rows", got)
	}
	m, _ = press(m, "esc", "/")
	m = typeStr(m, "RUN 10.0.0.2")
	if got := len(m.top().filtered); got != 1 || m.top().filtered[0].Name != "etcd-control-plane" {
		t.Errorf("multi-term case-insensitive filter got %d rows", got)
	}
	m, _ = press(m, "esc", "/")
	m = typeStr(m, "cre") // scattered letters of coredns must not match
	if got := len(m.top().filtered); got != 0 {
		t.Errorf("'cre' matched %d rows; substring filter expected", got)
	}
}
