package ui

import (
	"testing"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

func TestCompare(t *testing.T) {
	less := [][2]string{
		{"52s", "142m"}, {"142m", "2d19h"}, {"5h30m", "2d19h"}, {"3d1h", "2y3d"},
		{"3", "12"}, {"1 (142m ago)", "2"}, {"0", "1 (5m ago)"},
		{"<none>", "a"}, {"", "0"},
		{"alpha", "Beta"}, {"Pending", "running"},
	}
	for _, p := range less {
		if Compare(p[0], p[1]) >= 0 {
			t.Errorf("Compare(%q, %q) should be < 0", p[0], p[1])
		}
		if Compare(p[1], p[0]) <= 0 {
			t.Errorf("Compare(%q, %q) should be > 0", p[1], p[0])
		}
	}
	if Compare("Running", "Running") != 0 || Compare("<none>", "") != 0 {
		t.Error("equal values should compare 0")
	}
}

func TestSortRows(t *testing.T) {
	rows := []k8s.Row{
		{Name: "a", Cells: []string{"a", "12"}},
		{Name: "b", Cells: []string{"b", "3"}},
		{Name: "c", Cells: []string{"c", "<none>"}},
		{Name: "d", Cells: []string{"d"}},
	}
	SortRows(rows, 1, false)
	if got := rows[0].Name + rows[1].Name + rows[2].Name + rows[3].Name; got != "cdba" {
		t.Errorf("asc order = %s", got)
	}
	SortRows(rows, 1, true)
	if got := rows[0].Name + rows[1].Name; got != "ab" {
		t.Errorf("desc order starts %s", got)
	}
}
