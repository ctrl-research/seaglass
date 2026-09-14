package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
)

func TestRenderHelp(t *testing.T) {
	secs := []HelpSection{
		{"Global", []key.Binding{key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")), key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys"))}},
		{"Table", []key.Binding{key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter rows"))}},
	}
	out := stripANSI(RenderHelp(secs, 100, 20))
	for _, want := range []string{"Global", "q  quit", "?  keys", "Table", "/  filter rows"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
	if lines := strings.Count(out, "\n") + 1; lines != 20 {
		t.Errorf("help has %d lines, want 20", lines)
	}
	// Two sections side by side at width 100: both titles on the same line.
	first := strings.SplitN(out, "\n", 3)[1]
	if !strings.Contains(first, "Global") || !strings.Contains(first, "Table") {
		t.Errorf("sections should be in columns at width 100:\n%s", out)
	}
	narrow := stripANSI(RenderHelp(secs, 50, 20))
	if l := strings.SplitN(narrow, "\n", 3)[1]; strings.Contains(l, "Table") {
		t.Errorf("sections should stack at width 50:\n%s", narrow)
	}
}
