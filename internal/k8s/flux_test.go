package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxInventory(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "default_podinfo__Service", "v": "v1"},
			map[string]any{"id": "default_podinfo_apps_Deployment", "v": "v1"},
			map[string]any{"id": "flux-system_infra_kustomize.toolkit.fluxcd.io_Kustomization", "v": "v1"},
		}}},
	}}
	inv := FluxInventory(u)
	if len(inv) != 3 {
		t.Fatalf("got %d entries", len(inv))
	}
	if inv[0] != (ObjectRef{Namespace: "default", Name: "podinfo", Group: "", Kind: "Service"}) {
		t.Errorf("core entry parsed wrong: %+v", inv[0])
	}
	if inv[1].Group != "apps" || inv[1].Kind != "Deployment" {
		t.Errorf("apps entry: %+v", inv[1])
	}
	if inv[2].Group != "kustomize.toolkit.fluxcd.io" || inv[2].Kind != "Kustomization" {
		t.Errorf("dotted group entry: %+v", inv[2])
	}
}

func TestFluxDetail(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "apps", "namespace": "flux-system"},
		"spec": map[string]any{
			"suspend":   false,
			"sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"},
			"dependsOn": []any{map[string]any{"name": "infra"}},
		},
		"status": map[string]any{
			"lastAppliedRevision":   "main@sha1:abc123",
			"lastAttemptedRevision": "main@sha1:abc123",
			"conditions":            []any{map[string]any{"type": "Ready", "status": "True", "message": "Applied revision: main@sha1:abc123"}},
		},
	}}
	fi := FluxDetail(u)
	if fi.Ready != "True" || fi.AppliedRevision != "main@sha1:abc123" {
		t.Errorf("ready/applied wrong: %+v", fi)
	}
	if fi.Source == nil || fi.Source.Kind != "GitRepository" || fi.Source.Namespace != "flux-system" {
		t.Errorf("source: %+v", fi.Source)
	}
	if len(fi.DependsOn) != 1 || fi.DependsOn[0].Name != "infra" || fi.DependsOn[0].Namespace != "flux-system" {
		t.Errorf("dependsOn: %+v", fi.DependsOn)
	}
}
