package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func deploy(gen, observed, spec, updated, replicas, available int64, progressReason string) *unstructured.Unstructured {
	conds := []any{}
	if progressReason != "" {
		conds = append(conds, map[string]any{"type": "Progressing", "reason": progressReason})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"kind":     "Deployment",
		"metadata": map[string]any{"name": "web", "generation": gen},
		"spec":     map[string]any{"replicas": spec},
		"status": map[string]any{
			"observedGeneration": observed, "updatedReplicas": updated,
			"replicas": replicas, "availableReplicas": available, "conditions": conds,
		},
	}}
}

func TestDeploymentRollout(t *testing.T) {
	cases := []struct {
		name string
		obj  *unstructured.Unstructured
		want string
		done bool
		fail bool
	}{
		{"complete", deploy(8, 8, 2, 2, 2, 2, ""), "successfully rolled out", true, false},
		{"spec not observed", deploy(9, 8, 2, 2, 2, 2, ""), "spec update to be observed", false, false},
		{"updating", deploy(2, 2, 3, 1, 3, 1, ""), "1 out of 3 new replicas have been updated", false, false},
		{"old terminating", deploy(2, 2, 2, 2, 3, 2, ""), "1 old replicas are pending termination", false, false},
		{"not available", deploy(2, 2, 3, 3, 3, 1, ""), "1 of 3 updated replicas are available", false, false},
		{"deadline", deploy(2, 2, 3, 1, 3, 1, "ProgressDeadlineExceeded"), "exceeded its progress deadline", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RolloutStatus(c.obj)
			if !contains(got.Message, c.want) {
				t.Errorf("message = %q, want substring %q", got.Message, c.want)
			}
			if got.Done != c.done || got.Failed != c.fail {
				t.Errorf("done=%v failed=%v, want done=%v failed=%v", got.Done, got.Failed, c.done, c.fail)
			}
		})
	}
}

func TestRolloutable(t *testing.T) {
	if !Rolloutable(Resource{GVR: schemaGVR("apps", "deployments")}) {
		t.Error("deployments should be rolloutable")
	}
	if Rolloutable(Pods) {
		t.Error("pods are not rolloutable")
	}
}

func schemaGVR(group, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: "v1", Resource: resource}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
