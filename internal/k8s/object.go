package k8s

import (
	"context"
	"encoding/json"

	"fmt"
	"time"

	k8sjson "k8s.io/apimachinery/pkg/util/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Get fetches one object as unstructured JSON. namespace is ignored for
// cluster-scoped resources.
func (c *Client) Get(ctx context.Context, res Resource, namespace, name string) (*unstructured.Unstructured, error) {
	if !res.Namespaced {
		namespace = ""
	}
	gctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	segs := append(resourcePath(res.GVR, namespace), name)
	raw, err := c.rest.Get().AbsPath(segs...).Do(gctx).Raw()
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", res.Name(), name, err)
	}
	u := &unstructured.Unstructured{}
	if err := k8sjson.Unmarshal(raw, &u.Object); err != nil {
		return nil, fmt.Errorf("decode %s/%s: %w", res.Name(), name, err)
	}
	return u, nil
}

// ToYAML renders an object the way kubectl get -o yaml does, with
// managedFields removed since they are noise for a human reader.
func ToYAML(u *unstructured.Unstructured) (string, error) {
	cp := u.DeepCopy()
	unstructured.RemoveNestedField(cp.Object, "metadata", "managedFields")
	b, err := yaml.Marshal(cp.Object)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ServerVersion returns the API server's git version, e.g. "v1.30.1".
func (c *Client) ServerVersion(ctx context.Context) (string, error) {
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := c.rest.Get().AbsPath("version").Do(vctx).Raw()
	if err != nil {
		return "", fmt.Errorf("server version: %w", err)
	}
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("decode server version: %w", err)
	}
	return v.GitVersion, nil
}

// OwnerRef is a reference to an owning object.
type OwnerRef struct {
	APIVersion string
	Kind       string
	Name       string
	Controller bool
}

// ControllerOwner returns the controller owner of an object, or the first
// owner when none is marked controller. ok is false when there are none.
func ControllerOwner(u *unstructured.Unstructured) (OwnerRef, bool) {
	owners := u.GetOwnerReferences()
	if len(owners) == 0 {
		return OwnerRef{}, false
	}
	pick := owners[0]
	for _, o := range owners {
		if o.Controller != nil && *o.Controller {
			pick = o
			break
		}
	}
	ctrl := pick.Controller != nil && *pick.Controller
	return OwnerRef{APIVersion: pick.APIVersion, Kind: pick.Kind, Name: pick.Name, Controller: ctrl}, true
}

// ResourceByKind finds a discovered resource matching a kind and, when
// non-empty, an apiVersion (group/version or "v1"). Prefers an exact
// group match. ok is false when nothing matches.
func ResourceByKind(resources []Resource, kind, apiVersion string) (Resource, bool) {
	group := ""
	if apiVersion != "" && apiVersion != "v1" {
		if i := indexByte(apiVersion, '/'); i >= 0 {
			group = apiVersion[:i]
		}
	}
	var fallback Resource
	haveFallback := false
	for _, r := range resources {
		if r.Kind != kind {
			continue
		}
		if r.GVR.Group == group {
			return r, true
		}
		if !haveFallback {
			fallback, haveFallback = r, true
		}
	}
	return fallback, haveFallback
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// LabelSelectorString returns a "k=v,k2=v2" selector for an object that
// carries one: a Service's spec.selector map, or a workload's
// spec.selector.matchLabels. ok is false when there is no usable selector.
func LabelSelectorString(u *unstructured.Unstructured) (string, bool) {
	// Workloads: spec.selector.matchLabels
	if ml, found, _ := unstructured.NestedStringMap(u.Object, "spec", "selector", "matchLabels"); found && len(ml) > 0 {
		return joinSelector(ml), true
	}
	// Service: spec.selector is a flat map
	if sel, found, _ := unstructured.NestedStringMap(u.Object, "spec", "selector"); found && len(sel) > 0 {
		return joinSelector(sel), true
	}
	return "", false
}

// PodNodeName returns the node a pod is scheduled on.
func PodNodeName(u *unstructured.Unstructured) (string, bool) {
	n, found, _ := unstructured.NestedString(u.Object, "spec", "nodeName")
	return n, found && n != ""
}

// JoinLabelSelector renders a label map as "k=v,k2=v2", sorted.
func JoinLabelSelector(m map[string]string) string { return joinSelector(m) }

func joinSelector(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return joinComma(parts)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// NestedRef reads an object reference (kind/name, optional namespace) at a
// dotted path. apiGroup is read when present. ok is false if name is empty.
func NestedRef(u *unstructured.Unstructured, path string) (kind, name, namespace, apiGroup string, ok bool) {
	segs := splitDot(path)
	m, found, _ := unstructured.NestedMap(u.Object, segs...)
	if !found {
		return "", "", "", "", false
	}
	kind, _ = m["kind"].(string)
	name, _ = m["name"].(string)
	namespace, _ = m["namespace"].(string)
	// Flux sourceRef has no apiGroup; some refs carry apiGroup or apiVersion.
	if g, ok := m["apiGroup"].(string); ok {
		apiGroup = g
	} else if av, ok := m["apiVersion"].(string); ok {
		if i := indexByte(av, '/'); i >= 0 {
			apiGroup = av[:i]
		}
	}
	return kind, name, namespace, apiGroup, name != ""
}

// Label returns a metadata label value.
func Label(u *unstructured.Unstructured, key string) (string, bool) {
	v, ok := u.GetLabels()[key]
	return v, ok
}

// ResourceByKindGroup finds a discovered resource by kind and exact group.
func ResourceByKindGroup(resources []Resource, kind, group string) (Resource, bool) {
	var fallback Resource
	haveFallback := false
	for _, r := range resources {
		if r.Kind != kind {
			continue
		}
		if r.GVR.Group == group {
			return r, true
		}
		if !haveFallback {
			fallback, haveFallback = r, true
		}
	}
	return fallback, haveFallback
}

func splitDot(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}
