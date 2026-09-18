package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// IsFlux reports whether a resource belongs to the Flux toolkit.
func IsFlux(res Resource) bool {
	return strings.HasSuffix(res.GVR.Group, ".toolkit.fluxcd.io")
}

// ObjectRef is a namespaced reference to an object of a kind/group.
type ObjectRef struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// FluxInfo is the Flux-specific detail extracted from a Kustomization or
// HelmRelease for the revision-first detail header.
type FluxInfo struct {
	Ready             string // Ready condition status
	ReadyMessage      string
	Suspended         bool
	Source            *ObjectRef // spec.sourceRef (Kustomization) or chart source
	AppliedRevision   string
	AttemptedRevision string
	LastAppliedName   string // HelmRelease: last applied chart version
	DependsOn         []ObjectRef
	Inventory         []ObjectRef
	ManagedBy         *ObjectRef // from kustomize/helm labels, if any
}

// FluxDetail extracts Flux status detail from an object. Fields that do not
// apply are left zero.
func FluxDetail(u *unstructured.Unstructured) FluxInfo {
	var fi FluxInfo
	fi.Ready, fi.ReadyMessage = ReadyCondition(u)
	fi.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	fi.AppliedRevision, _, _ = unstructured.NestedString(u.Object, "status", "lastAppliedRevision")
	fi.AttemptedRevision, _, _ = unstructured.NestedString(u.Object, "status", "lastAttemptedRevision")

	if k, n, ns, group, ok := NestedRef(u, "spec.sourceRef"); ok {
		fi.Source = &ObjectRef{Group: group, Kind: k, Name: n, Namespace: orNs(ns, u.GetNamespace())}
	} else if k, n, ns, group, ok := NestedRef(u, "spec.chart.spec.sourceRef"); ok {
		fi.Source = &ObjectRef{Group: group, Kind: k, Name: n, Namespace: orNs(ns, u.GetNamespace())}
	}

	if deps, found, _ := unstructured.NestedSlice(u.Object, "spec", "dependsOn"); found {
		for _, d := range deps {
			m, ok := d.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			ns, _ := m["namespace"].(string)
			fi.DependsOn = append(fi.DependsOn, ObjectRef{Kind: u.GetKind(), Group: groupOf(u), Name: name, Namespace: orNs(ns, u.GetNamespace())})
		}
	}

	fi.Inventory = FluxInventory(u)

	if name, ok := Label(u, "kustomize.toolkit.fluxcd.io/name"); ok {
		ns, _ := Label(u, "kustomize.toolkit.fluxcd.io/namespace")
		fi.ManagedBy = &ObjectRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Name: name, Namespace: ns}
	} else if name, ok := Label(u, "helm.toolkit.fluxcd.io/name"); ok {
		ns, _ := Label(u, "helm.toolkit.fluxcd.io/namespace")
		fi.ManagedBy = &ObjectRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Name: name, Namespace: ns}
	}
	return fi
}

// FluxInventory parses status.inventory.entries. Each id is
// "namespace_name_group_kind" (group empty for core); none of those fields
// can contain an underscore, so a 4-way split is exact.
func FluxInventory(u *unstructured.Unstructured) []ObjectRef {
	entries, found, _ := unstructured.NestedSlice(u.Object, "status", "inventory", "entries")
	if !found {
		return nil
	}
	var out []ObjectRef
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		parts := strings.SplitN(id, "_", 4)
		if len(parts) != 4 {
			continue
		}
		out = append(out, ObjectRef{Namespace: parts[0], Name: parts[1], Group: parts[2], Kind: parts[3]})
	}
	return out
}

func orNs(ns, fallback string) string {
	if ns == "" {
		return fallback
	}
	return ns
}

func groupOf(u *unstructured.Unstructured) string {
	av := u.GetAPIVersion()
	if i := strings.IndexByte(av, '/'); i >= 0 {
		return av[:i]
	}
	return ""
}
