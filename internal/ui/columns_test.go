package ui

import (
	"testing"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

func TestFitColumnsDropsWideColumnsFirst(t *testing.T) {
	cols := []k8s.Column{{Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"}, {Name: "IP", Priority: 1}, {Name: "NODE", Priority: 1}}
	rows := []k8s.Row{{Cells: []string{"nginx-abcdef-12345", "1/1", "Running", "10.0.0.1", "ip-10-0-0-1.ec2.internal"}}}

	tc, idx := FitColumns(cols, rows, 40)
	if len(idx) != 3 {
		t.Fatalf("at width 40 expected only priority-0 columns, got %v", idx)
	}
	total := 0
	for _, c := range tc {
		total += c.Width + cellPad
	}
	if total != 40 {
		t.Errorf("columns should fill width exactly, got %d", total)
	}

	_, idx = FitColumns(cols, rows, 120)
	if len(idx) != 5 {
		t.Errorf("at width 120 expected all columns, got %v", idx)
	}
	for i := 1; i < len(idx); i++ {
		if idx[i] < idx[i-1] {
			t.Errorf("indices not in server order: %v", idx)
		}
	}
}

func TestFitColumnsShrinksWhenRequiredOverflow(t *testing.T) {
	cols := []k8s.Column{{Name: "NAME"}, {Name: "STATUS"}}
	rows := []k8s.Row{{Cells: []string{"a-very-long-pod-name-that-will-not-fit-anywhere", "Running"}}}
	tc, _ := FitColumns(cols, rows, 30)
	total := 0
	for _, c := range tc {
		total += c.Width + cellPad
	}
	if total > 30 {
		t.Errorf("required columns overflow: %d > 30", total)
	}
}

func TestStatusBarWidth(t *testing.T) {
	s := StatusBar{Context: "homelab", Namespace: "default", Crumbs: []string{"pods", "deployments"}, Count: "12 rows", Hint: ": palette  esc back  q quit", State: "live"}
	for _, w := range []int{40, 80, 200} {
		if got := visibleWidth(s.Render(w)); got != w {
			t.Errorf("width %d: rendered %d", w, got)
		}
	}
}

func TestStatusBarFilteredCount(t *testing.T) {
	s := StatusBar{Context: "c", Namespace: "n", Crumbs: []string{"pods"}, Count: "2 of 9 rows", State: "live"}
	out := s.Render(80)
	if !contains(out, "2 of 9 rows") {
		t.Errorf("expected filtered count, got %q", out)
	}
	s.Count = ""
	if out := stripANSI(s.Render(80)); contains(out, "rows") {
		t.Errorf("empty count should hide rows, got %q", out)
	}
}
