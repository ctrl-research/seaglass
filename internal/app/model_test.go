package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

func testModel() Model {
	return New(Options{
		Client:   &k8s.Client{Context: "test-ctx", Namespace: "default"},
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
	})
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

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := testModel()
		var msg tea.KeyPressMsg
		if k == "q" {
			msg = tea.KeyPressMsg{Code: 'q', Text: "q"}
		} else {
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		}
		_, cmd := m.Update(msg)
		if cmd == nil {
			t.Fatalf("%s: expected quit cmd", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s: expected QuitMsg", k)
		}
	}
}

func TestRendersRowsAndStatus(t *testing.T) {
	m := testModel()
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = mm.(Model)
	mm, cmd := m.Update(updateMsg(k8s.Update{Snapshot: snap(), Status: k8s.StatusLive}))
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("expected model to re-arm the wait command")
	}
	out := m.View().Content
	for _, want := range []string{"NAME", "STATUS", "Running", "Pending", "test-ctx", "pods", "3 rows", "live"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q\n%s", want, out)
		}
	}
	if lines := strings.Count(out, "\n") + 1; lines != 20 {
		t.Errorf("view has %d lines, want 20", lines)
	}
}

func TestSelectionSurvivesUpdate(t *testing.T) {
	m := testModel()
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = mm.(Model)
	mm, _ = m.Update(updateMsg(k8s.Update{Snapshot: snap(), Status: k8s.StatusLive}))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mm.(Model)
	if m.selectedKey() != "2" {
		t.Fatalf("selected %q after down, want b", m.selectedKey())
	}
	// New row sorts before b; selection should stay on b.
	s := snap()
	s.Rows = append([]k8s.Row{{Name: "0", UID: "0", Cells: []string{"0", "Running", ""}}}, s.Rows...)
	mm, _ = m.Update(updateMsg(k8s.Update{Snapshot: s, Status: k8s.StatusLive}))
	m = mm.(Model)
	if m.selectedKey() != "2" {
		t.Errorf("selection moved to %q after insert above", m.selectedKey())
	}
}
