package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEditableYAMLStripsVolatileFields(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": "a", "namespace": "default", "resourceVersion": "42",
			"creationTimestamp": "2026-09-14T10:00:00Z", "generation": int64(3),
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
		"spec":   map[string]any{"replicas": int64(2)},
		"status": map[string]any{"phase": "Running"},
	}}
	out, err := EditableYAML(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"managedFields", "creationTimestamp", "generation", "status:", "phase"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q should be stripped:\n%s", gone, out)
		}
	}
	// resourceVersion is kept so the update can detect a conflict.
	if !strings.Contains(out, "resourceVersion: \"42\"") || !strings.Contains(out, "replicas: 2") {
		t.Errorf("kept fields missing:\n%s", out)
	}
	// Input is not mutated.
	if _, ok := u.Object["status"]; !ok {
		t.Error("EditableYAML must not mutate its input")
	}
}
