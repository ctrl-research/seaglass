package k8s

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Rolloutable reports whether a resource type has a rollout to track.
func Rolloutable(res Resource) bool {
	switch res.GVR.Group + "/" + res.GVR.Resource {
	case "apps/deployments", "apps/statefulsets", "apps/daemonsets":
		return true
	}
	return false
}

// RolloutState is a point-in-time rollout status.
type RolloutState struct {
	Message string
	Done    bool // rollout complete
	Failed  bool // rollout will not complete (e.g. deadline exceeded)
}

// RolloutStatus computes a kubectl-style rollout status from an object's
// status subresource. It mirrors kubectl rollout status for Deployments,
// StatefulSets, and DaemonSets.
func RolloutStatus(u *unstructured.Unstructured) RolloutState {
	switch u.GetKind() {
	case "Deployment":
		return deploymentRollout(u)
	case "StatefulSet":
		return statefulSetRollout(u)
	case "DaemonSet":
		return daemonSetRollout(u)
	default:
		return RolloutState{Message: "no rollout status for " + u.GetKind(), Done: true}
	}
}

func nestedInt(u *unstructured.Unstructured, fields ...string) int64 {
	v, _, _ := unstructured.NestedInt64(u.Object, fields...)
	return v
}

func specReplicas(u *unstructured.Unstructured) int64 {
	if v, found, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); found {
		return v
	}
	return 1
}

func deploymentRollout(u *unstructured.Unstructured) RolloutState {
	gen := nestedInt(u, "metadata", "generation")
	observed := nestedInt(u, "status", "observedGeneration")
	if observed < gen {
		return RolloutState{Message: "Waiting for deployment spec update to be observed..."}
	}
	// A Progressing=False with ProgressDeadlineExceeded means it failed.
	if conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions"); conds != nil {
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cm["type"] == "Progressing" && cm["reason"] == "ProgressDeadlineExceeded" {
				return RolloutState{Message: fmt.Sprintf("deployment %q exceeded its progress deadline", u.GetName()), Failed: true}
			}
		}
	}
	spec := specReplicas(u)
	updated := nestedInt(u, "status", "updatedReplicas")
	total := nestedInt(u, "status", "replicas")
	available := nestedInt(u, "status", "availableReplicas")
	switch {
	case updated < spec:
		return RolloutState{Message: fmt.Sprintf("Waiting for rollout to finish: %d out of %d new replicas have been updated...", updated, spec)}
	case total > updated:
		return RolloutState{Message: fmt.Sprintf("Waiting for rollout to finish: %d old replicas are pending termination...", total-updated)}
	case available < updated:
		return RolloutState{Message: fmt.Sprintf("Waiting for rollout to finish: %d of %d updated replicas are available...", available, updated)}
	default:
		return RolloutState{Message: fmt.Sprintf("deployment %q successfully rolled out", u.GetName()), Done: true}
	}
}

func statefulSetRollout(u *unstructured.Unstructured) RolloutState {
	if strategy, _, _ := unstructured.NestedString(u.Object, "spec", "updateStrategy", "type"); strategy != "" && strategy != "RollingUpdate" {
		return RolloutState{Message: fmt.Sprintf("updateStrategy %q does not have a status", strategy), Done: true}
	}
	gen := nestedInt(u, "metadata", "generation")
	observed := nestedInt(u, "status", "observedGeneration")
	if observed == 0 || gen > observed {
		return RolloutState{Message: "Waiting for statefulset spec update to be observed..."}
	}
	spec := specReplicas(u)
	ready := nestedInt(u, "status", "readyReplicas")
	if ready < spec {
		return RolloutState{Message: fmt.Sprintf("Waiting for %d pods to be ready...", spec-ready)}
	}
	updated := nestedInt(u, "status", "updatedReplicas")
	if updated < spec {
		return RolloutState{Message: fmt.Sprintf("Waiting for rollout to finish: %d out of %d new pods have been updated...", updated, spec)}
	}
	return RolloutState{Message: fmt.Sprintf("statefulset rolling update complete: %d pods at revision", spec), Done: true}
}

func daemonSetRollout(u *unstructured.Unstructured) RolloutState {
	if strategy, _, _ := unstructured.NestedString(u.Object, "spec", "updateStrategy", "type"); strategy != "" && strategy != "RollingUpdate" {
		return RolloutState{Message: fmt.Sprintf("updateStrategy %q does not have a status", strategy), Done: true}
	}
	gen := nestedInt(u, "metadata", "generation")
	observed := nestedInt(u, "status", "observedGeneration")
	if observed == 0 || gen > observed {
		return RolloutState{Message: "Waiting for daemon set spec update to be observed..."}
	}
	desired := nestedInt(u, "status", "desiredNumberScheduled")
	updated := nestedInt(u, "status", "updatedNumberScheduled")
	available := nestedInt(u, "status", "numberAvailable")
	if updated < desired {
		return RolloutState{Message: fmt.Sprintf("Waiting for daemon set rollout to finish: %d out of %d new pods have been updated...", updated, desired)}
	}
	if available < desired {
		return RolloutState{Message: fmt.Sprintf("Waiting for daemon set rollout to finish: %d of %d updated pods are available...", available, desired)}
	}
	return RolloutState{Message: fmt.Sprintf("daemon set rolled out: %d pods available", desired), Done: true}
}
