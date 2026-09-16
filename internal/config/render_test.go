package config

import (
	"testing"
	"time"
)

func TestRenderPatchCoercesTypes(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	obj := map[string]any{"spec": map[string]any{"replicas": int64(3)}}
	data := NewRenderData(obj, "5", now)

	patch := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"reconcile.fluxcd.io/requestedAt": "{{now}}"}},
		"spec":     map[string]any{"replicas": "{{.input}}", "suspend": "{{.spec.replicas}}", "paused": true, "note": "true"},
	}
	out, err := RenderPatch(patch, data)
	if err != nil {
		t.Fatal(err)
	}
	spec := out["spec"].(map[string]any)
	if spec["replicas"] != int64(5) {
		t.Errorf("replicas = %#v, want int64(5)", spec["replicas"])
	}
	if spec["suspend"] != int64(3) {
		t.Errorf("suspend template = %#v", spec["suspend"])
	}
	if spec["paused"] != true {
		t.Errorf("paused (yaml bool) = %#v, want true", spec["paused"])
	}
	if spec["note"] != "true" {
		t.Errorf("note (literal string) = %#v, want \"true\"", spec["note"])
	}
	ann := out["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann["reconcile.fluxcd.io/requestedAt"] != "2026-09-16T12:00:00Z" {
		t.Errorf("now = %v", ann["reconcile.fluxcd.io/requestedAt"])
	}
}

func TestRenderString(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	obj := map[string]any{"status": map[string]any{"replicas": int64(2)}}
	got, err := RenderString("{{.status.replicas}}", NewRenderData(obj, "", now))
	if err != nil {
		t.Fatal(err)
	}
	if got != "2" {
		t.Errorf("got %q", got)
	}
	if got, _ := RenderString("plain", NewRenderData(obj, "", now)); got != "plain" {
		t.Errorf("plain string changed: %q", got)
	}
}
