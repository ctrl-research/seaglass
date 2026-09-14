package k8s

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseLogLine(t *testing.T) {
	l := parseLogLine("app", "2026-09-14T20:43:53.123456789Z GET /healthz 200")
	if l.Container != "app" || l.Text != "GET /healthz 200" || l.Time.IsZero() || l.Time.Nanosecond() != 123456789 {
		t.Errorf("parsed %+v", l)
	}
	l = parseLogLine("app", "no timestamp here")
	if !l.Time.IsZero() || l.Text != "no timestamp here" {
		t.Errorf("continuation parsed %+v", l)
	}
	l = parseLogLine("app", "2026-09-14T20:43:53Z")
	if l.Time.IsZero() && l.Text != "2026-09-14T20:43:53Z" {
		t.Errorf("lone timestamp parsed %+v", l)
	}
	_ = time.Now
}

func TestContainers(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"initContainers":      []any{map[string]any{"name": "init"}},
		"containers":          []any{map[string]any{"name": "app"}, map[string]any{"name": "sidecar"}},
		"ephemeralContainers": []any{map[string]any{"name": "debug"}},
	}}}
	got := Containers(pod)
	want := []string{"init", "app", "sidecar", "debug"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v want %v", got, want)
		}
	}
}
