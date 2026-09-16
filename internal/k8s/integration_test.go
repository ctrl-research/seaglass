//go:build integration

package k8s

import (
	"context"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestStreamLive lists and watches pods on a real cluster. Run with:
//
//	SEAGLASS_TEST_CONTEXT=colima go test -tags integration ./internal/k8s/ -run TestStreamLive -v
func TestStreamLive(t *testing.T) {
	ctxName := os.Getenv("SEAGLASS_TEST_CONTEXT")
	if ctxName == "" {
		t.Skip("SEAGLASS_TEST_CONTEXT not set")
	}
	c, err := New(ctxName, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	ch := c.Stream(ctx, pods, "", "", "", false)
	for u := range ch {
		if u.Err != nil {
			t.Fatalf("stream error: %v", u.Err)
		}
		if u.Status == StatusLive {
			t.Logf("context=%s ns=%s columns=%d rows=%d", c.Context, c.Namespace, len(u.Snapshot.Columns), len(u.Snapshot.Rows))
			for _, col := range u.Snapshot.Columns {
				t.Logf("  column %q priority=%d", col.Name, col.Priority)
			}
			for i, r := range u.Snapshot.Rows {
				if i >= 3 {
					break
				}
				t.Logf("  row ns=%s name=%s uid=%s cells=%v", r.Namespace, r.Name, r.UID, r.Cells)
			}
			if len(u.Snapshot.Columns) == 0 {
				t.Fatal("no columns returned; Table transform not honored")
			}
			return
		}
	}
	t.Fatal("stream ended without going live")
}

func TestDiscoveryLive(t *testing.T) {
	ctxName := os.Getenv("SEAGLASS_TEST_CONTEXT")
	if ctxName == "" {
		t.Skip("SEAGLASS_TEST_CONTEXT not set")
	}
	c, err := New(ctxName, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	rs, err := c.Resources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d resources in %s (first call, cold or warm cache)", len(rs), time.Since(start))
	start = time.Now()
	if _, err := c.Resources(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("second call %s", time.Since(start))

	var sawPods, sawDeploy, sawNodes bool
	for _, r := range rs {
		switch r.GVR.Resource {
		case "pods":
			sawPods = r.Namespaced && r.GVR.Group == ""
		case "deployments":
			sawDeploy = r.GVR.Group == "apps" && r.Namespaced
		case "nodes":
			sawNodes = !r.Namespaced
		}
	}
	if !sawPods || !sawDeploy || !sawNodes {
		t.Errorf("missing expected resources: pods=%v deployments=%v nodes=%v", sawPods, sawDeploy, sawNodes)
	}
	if rs[0].GVR.Group != "" {
		t.Errorf("core group should sort first, got %v", rs[0].GVR)
	}

	names, err := c.NamespaceNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("namespaces: %v", names)
	if len(names) == 0 {
		t.Error("no namespaces")
	}
}
