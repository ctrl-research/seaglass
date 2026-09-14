package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestToYAMLStripsManagedFields(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":          "a",
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
		"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "image": "nginx"}}},
	}}
	out, err := ToYAML(u)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "managedFields") {
		t.Errorf("managedFields not stripped:\n%s", out)
	}
	if !strings.Contains(out, "image: nginx") || !strings.HasPrefix(out, "apiVersion: v1\n") {
		t.Errorf("unexpected yaml:\n%s", out)
	}
	if _, ok := u.Object["metadata"].(map[string]any)["managedFields"]; !ok {
		t.Error("ToYAML must not mutate its input")
	}
}
