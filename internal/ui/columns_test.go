package ui

import (
	"strings"
	"testing"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

func TestFitColumnsDropsWideColumnsFirst(t *testing.T) {
	cols := []k8s.Column{{Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"}, {Name: "IP", Priority: 1}, {Name: "NODE", Priority: 1}}
	rows := []k8s.Row{{Cells: []string{"nginx-abcdef-12345", "1/1", "Running", "10.0.0.1", "ip-10-0-0-1.ec2.internal"}}}

	tc, idx := FitColumns(cols, rows, 40, ColumnsAuto)
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

	_, idx = FitColumns(cols, rows, 120, ColumnsAuto)
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
	tc, _ := FitColumns(cols, rows, 30, ColumnsAuto)
	total := 0
	for _, c := range tc {
		total += c.Width + cellPad
	}
	if total > 30 {
		t.Errorf("required columns overflow: %d > 30", total)
	}
}

func TestStatusBarWidth(t *testing.T) {
	s := StatusBar{Context: "homelab", Namespace: "default", Crumbs: []string{"pods", "deployments"}, Count: "12 rows", Hint: ": palette  / filter  s sort  w columns  enter detail", Back: "esc back to pods", State: "live"}
	for _, w := range []int{40, 60, 80, 200} {
		out := s.Render(w)
		if got := visibleWidth(out); got != w {
			t.Errorf("width %d: rendered %d", w, got)
		}
		if strings.Contains(out, "\n") {
			t.Errorf("width %d: status bar wrapped", w)
		}
	}
	if !contains(stripANSI(s.Render(200)), "s sort") {
		t.Error("hint should show when there is room")
	}
	at80 := stripANSI(s.Render(80))
	if contains(at80, "s sort") || !contains(at80, "esc back to pods") {
		t.Errorf("at 80 the hint should drop before the back target: %q", at80)
	}
	if !contains(stripANSI(s.Render(60)), "12 rows") {
		t.Error("count should survive when the hint is dropped")
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

func TestFitColumnsModes(t *testing.T) {
	cols := []k8s.Column{{Name: "NAME"}, {Name: "STATUS"}, {Name: "IP", Priority: 1}, {Name: "NODE", Priority: 1}}
	rows := []k8s.Row{{Cells: []string{"nginx-abcdef-12345", "Running", "10.0.0.1", "ip-10-0-0-1.ec2.internal"}}}
	if _, idx := FitColumns(cols, rows, 40, ColumnsWide); len(idx) != 4 {
		t.Errorf("wide should include all columns at any width, got %v", idx)
	}
	tc, _ := FitColumns(cols, rows, 40, ColumnsWide)
	total := 0
	for _, c := range tc {
		total += c.Width + cellPad
	}
	if total > 40 {
		t.Errorf("wide columns should shrink to fit: %d > 40", total)
	}
	if _, idx := FitColumns(cols, rows, 200, ColumnsNarrow); len(idx) != 2 {
		t.Errorf("narrow should drop optional columns, got %v", idx)
	}
	if ColumnsAuto.Next() != ColumnsWide || ColumnsWide.Next() != ColumnsNarrow || ColumnsNarrow.Next() != ColumnsAuto {
		t.Error("mode cycle wrong")
	}
}
