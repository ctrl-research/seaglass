package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	if err := json.Unmarshal(raw, &u.Object); err != nil {
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
