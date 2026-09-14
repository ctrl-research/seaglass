package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestHeaderRender(t *testing.T) {
	h := Header{Fields: []Field{
		{"context", "kind-seaglass-dev"}, {"namespace", "all"}, {"user", "kind-user"},
		{"cluster", "https://127.0.0.1:6443"}, {"k8s", "v1.30.0"}, {"seaglass", "v0.0.1"},
	}}
	out := h.Render(120)
	lines := strings.Split(out, "\n")
	if len(lines) != HeaderHeight {
		t.Fatalf("header has %d lines, want %d", len(lines), HeaderHeight)
	}
	plain := stripANSI(out)
	for _, want := range []string{"/ ___|  ___", "|___/", "context: kind-seaglass-dev", "namespace: all", "k8s: v1.30.0", "seaglass: v0.0.1"} {
		if !strings.Contains(plain, want) {
			t.Errorf("header missing %q:\n%s", want, plain)
		}
	}
	if w := lipgloss.Width(lines[6]); w != 120 {
		t.Errorf("rule width %d, want 120", w)
	}
	for i, l := range lines[:6] {
		if w := lipgloss.Width(l); w > 120 {
			t.Errorf("line %d overflows: %d", i, w)
		}
	}
}

func TestHeaderDropsFieldsWhenNarrow(t *testing.T) {
	h := Header{Fields: []Field{{"context", "ctx"}, {"cluster", "https://example:6443"}}}
	plain := stripANSI(h.Render(60))
	if strings.Contains(plain, "cluster") || !strings.Contains(plain, "|___/") {
		t.Errorf("fields should be dropped but the logo kept at width 60:\n%s", plain)
	}
	if !h.Visible(60, 30) || h.Visible(60, 12) || h.Visible(40, 30) {
		t.Error("visibility thresholds wrong")
	}
}
