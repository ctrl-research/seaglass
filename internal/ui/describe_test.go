package ui

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func pod() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":              "web-1",
			"namespace":         "default",
			"uid":               "abc",
			"creationTimestamp": "2026-09-14T10:00:00Z",
			"labels":            map[string]any{"app": "web", "tier": "front"},
			"ownerReferences":   []any{map[string]any{"kind": "ReplicaSet", "name": "web-abc"}},
		},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "nginx", "image": "nginx:1.27", "ports": []any{map[string]any{"containerPort": float64(80), "protocol": "TCP"}}},
			},
		},
		"status": map[string]any{
			"phase": "Running",
			"podIP": "10.0.0.5",
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
				map[string]any{"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "message": "0/3 nodes"},
			},
			"containerStatuses": []any{map[string]any{"name": "nginx"}},
		},
	}}
}

func TestDescribe(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	out := stripANSI(Describe(pod(), now))
	for _, want := range []string{
		"Kind:         Pod  v1",
		"Name:         web-1",
		"Namespace:    default",
		"120m ago",
		"app=web",
		"tier=front",
		"Owner:        ReplicaSet/web-abc",
		"Status",
		"phase:               Running",
		"podIP:               10.0.0.5",
		"Conditions",
		"Ready         True",
		"PodScheduled  False   Unschedulable  0/3 nodes",
		"Containers",
		"nginx  nginx:1.27  80/tcp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("describe missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "containerStatuses") {
		t.Error("nested status collections should be left to the YAML view")
	}
}

func TestDescribeMinimalObject(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "cm"}}}
	out := stripANSI(Describe(u, time.Now()))
	if !strings.Contains(out, "Labels:       <none>") || strings.Contains(out, "Status") {
		t.Errorf("minimal describe:\n%s", out)
	}
}

func TestColorizeYAMLPreservesText(t *testing.T) {
	src := "apiVersion: v1\nkind: Pod\nmetadata:\n  labels:\n    app: web\n  replicas: 3\n  ready: true\n  items:\n  - name: a\n  - 42\n# comment\n"
	out := ColorizeYAML(src)
	if stripANSI(out) != src {
		t.Errorf("colorizing changed the text:\n%q\n%q", stripANSI(out), src)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected some color codes")
	}
}

func TestStatusSeverity(t *testing.T) {
	cases := map[string]Severity{
		"Running": SevOK, "Completed": SevOK,
		"CrashLoopBackOff": SevError, "OOMKilled": SevError, "ImagePullBackOff": SevError, "Error": SevError,
		"Pending": SevWarn, "ContainerCreating": SevWarn, "Terminating": SevWarn,
		"Init:1/2": SevWarn, "Init:Error": SevError,
		"Bound": SevNone,
	}
	for v, want := range cases {
		if got := StatusSeverity(v); got != want {
			t.Errorf("StatusSeverity(%q) = %d, want %d", v, got, want)
		}
	}
	// StyleStatus adds color for classified values, none otherwise.
	if StyleStatus("CrashLoopBackOff") == "CrashLoopBackOff" {
		t.Error("error status should be colored")
	}
	if StyleStatus("Bound") != "Bound" {
		t.Error("unclassified status should be plain")
	}
}

func crashingPod() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "p", "namespace": "default"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "x"}}},
		"status": map[string]any{
			"containerStatuses": []any{map[string]any{
				"name": "app", "ready": false, "restartCount": int64(7),
				"state":     map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
				"lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled"}},
			}},
		},
	}}
}

func TestDescribeContainerStatus(t *testing.T) {
	out := stripANSI(Describe(crashingPod(), timeNow()))
	if !contains(out, "Container status") {
		t.Fatalf("missing container status section:\n%s", out)
	}
	for _, want := range []string{"app", "false", "7", "Waiting", "CrashLoopBackOff (OOMKilled)"} {
		if !contains(out, want) {
			t.Errorf("container status missing %q:\n%s", want, out)
		}
	}
	// The problem reason is colored in the non-stripped output.
	if stripANSI(Describe(crashingPod(), timeNow())) == Describe(crashingPod(), timeNow()) {
		t.Error("expected color codes in the container status")
	}
}
