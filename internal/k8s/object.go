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
