package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func withOwners(owners ...map[string]any) *unstructured.Unstructured {
	os := make([]any, len(owners))
	for i, o := range owners {
		os[i] = o
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p", "ownerReferences": os},
	}}
}

func TestControllerOwner(t *testing.T) {
	tru := true
	u := withOwners(
		map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "rs-1", "controller": tru},
	)
	o, ok := ControllerOwner(u)
	if !ok || o.Kind != "ReplicaSet" || o.Name != "rs-1" || !o.Controller {
		t.Fatalf("owner = %+v ok=%v", o, ok)
	}
	if _, ok := ControllerOwner(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{}}}); ok {
		t.Error("no owners should return ok=false")
	}
}

func TestResourceByKind(t *testing.T) {
	rs := []Resource{
		{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, Kind: "ReplicaSet"},
		{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Kind: "Deployment"},
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Kind: "Pod"},
	}
	r, ok := ResourceByKind(rs, "Deployment", "apps/v1")
	if !ok || r.GVR.Resource != "deployments" {
		t.Errorf("got %+v ok=%v", r, ok)
	}
	if r, ok := ResourceByKind(rs, "Pod", "v1"); !ok || r.GVR.Resource != "pods" {
		t.Errorf("core pod: %+v ok=%v", r, ok)
	}
	if _, ok := ResourceByKind(rs, "CronJob", "batch/v1"); ok {
		t.Error("unknown kind should return ok=false")
	}
}
