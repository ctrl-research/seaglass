package config

import (
	"path/filepath"
	"testing"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

func TestStateRoundTrip(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "state.json")}
	st, err := s.Load()
	if err != nil || st.LastContext != "" {
		t.Fatalf("missing file should load empty: %+v %v", st, err)
	}
	st.Update("homelab", ContextState{Namespace: "monitoring", Resource: &k8s.Pods})
	st.Update("prod", ContextState{AllNamespaces: true})
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastContext != "prod" {
		t.Errorf("last context = %q", got.LastContext)
	}
	hl, ok := got.For("homelab")
	if !ok || hl.Namespace != "monitoring" || hl.Resource == nil || hl.Resource.GVR.Resource != "pods" || !hl.Resource.Namespaced {
		t.Errorf("homelab state = %+v", hl)
	}
	if p, _ := got.For("prod"); !p.AllNamespaces || p.Namespace != "" {
		t.Errorf("prod state = %+v", p)
	}
}
