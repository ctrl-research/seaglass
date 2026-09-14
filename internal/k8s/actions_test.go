package k8s

import "testing"

func TestMergePatch(t *testing.T) {
	b, err := MergePatch([]Field{
		{Path: []string{"spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt"}, Value: "T"},
		{Path: []string{"spec", "replicas"}, Value: 3},
		{Path: []string{"metadata", "labels", "old"}, Value: nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"metadata":{"labels":{"old":null}},"spec":{"replicas":3,"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"T"}}}}}`
	if string(b) != want {
		t.Errorf("patch = %s\nwant    %s", b, want)
	}
	if _, err := MergePatch([]Field{{Value: 1}}); err == nil {
		t.Error("empty path should error")
	}
}
