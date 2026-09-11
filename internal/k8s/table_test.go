package k8s

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

func TestResourcePath(t *testing.T) {
	cases := []struct {
		gvr  schema.GroupVersionResource
		ns   string
		want string
	}{
		{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "default", "api/v1/namespaces/default/pods"},
		{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "", "api/v1/pods"},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "kube-system", "apis/apps/v1/namespaces/kube-system/deployments"},
		{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "", "apis/apps/v1/deployments"},
	}
	for _, c := range cases {
		got := ""
		for i, s := range resourcePath(c.gvr, c.ns) {
			if i > 0 {
				got += "/"
			}
			got += s
		}
		if got != c.want {
			t.Errorf("resourcePath(%v, %q) = %q, want %q", c.gvr, c.ns, got, c.want)
		}
	}
}

func TestFormatCell(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "<none>"},
		{"Running", "Running"},
		{float64(3), "3"},
		{float64(2.5), "2.5"},
		{int64(7), "7"},
		{true, "true"},
		{map[string]any{"a": 1.0}, `{"a":1}`},
	}
	for _, c := range cases {
		if got := formatCell(c.in); got != c.want {
			t.Errorf("formatCell(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func meta(ns, name, uid string) runtime.RawExtension {
	b, _ := json.Marshal(metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + uid)},
	})
	return runtime.RawExtension{Raw: b}
}

func TestStoreApply(t *testing.T) {
	st := newStore()
	st.reset(&metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{{Name: "Name"}, {Name: "Status"}, {Name: "IP", Priority: 1}},
		Rows: []metav1.TableRow{
			{Cells: []any{"b", "Running", "10.0.0.2"}, Object: meta("default", "b", "b")},
			{Cells: []any{"a", "Pending", nil}, Object: meta("default", "a", "a")},
		},
	})
	snap := st.snapshot()
	if len(snap.Columns) != 3 || snap.Columns[2].Priority != 1 {
		t.Fatalf("columns = %+v", snap.Columns)
	}
	if len(snap.Rows) != 2 || snap.Rows[0].Name != "a" || snap.Rows[1].Name != "b" {
		t.Fatalf("rows not sorted by name: %+v", snap.Rows)
	}
	if snap.Rows[0].Cells[2] != "<none>" {
		t.Errorf("nil cell = %q", snap.Rows[0].Cells[2])
	}

	// Modified event without column definitions keeps existing columns.
	st.apply(watch.Modified, &metav1.Table{Rows: []metav1.TableRow{
		{Cells: []any{"a", "Running", "10.0.0.1"}, Object: meta("default", "a", "a")},
	}})
	snap = st.snapshot()
	if len(snap.Columns) != 3 {
		t.Errorf("columns dropped after event without definitions")
	}
	if snap.Rows[0].Cells[1] != "Running" {
		t.Errorf("modified row not applied: %+v", snap.Rows[0])
	}

	st.apply(watch.Deleted, &metav1.Table{Rows: []metav1.TableRow{
		{Cells: []any{"b", "Terminated", ""}, Object: meta("default", "b", "b")},
	}})
	snap = st.snapshot()
	if len(snap.Rows) != 1 || snap.Rows[0].Name != "a" {
		t.Errorf("delete not applied: %+v", snap.Rows)
	}
}
