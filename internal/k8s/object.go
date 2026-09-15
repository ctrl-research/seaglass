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
