package k8s

import (
	"context"
	"encoding/json"

	"fmt"
	"time"

	k8sjson "k8s.io/apimachinery/pkg/util/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// Field is one value to set in a merge patch, addressed by path segments so
// keys containing dots (annotations) need no escaping.
type Field struct {
	Path  []string
	Value any
}

// MergePatch builds a JSON merge patch body from fields. A nil Value
// deletes the key, per RFC 7386.
func MergePatch(fields []Field) ([]byte, error) {
	root := map[string]any{}
	for _, f := range fields {
		if len(f.Path) == 0 {
			return nil, fmt.Errorf("empty field path")
		}
		m := root
		for _, seg := range f.Path[:len(f.Path)-1] {
			next, ok := m[seg].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[seg] = next
			}
			m = next
		}
		m[f.Path[len(f.Path)-1]] = f.Value
	}
	return json.Marshal(root)
}

// Patch applies a JSON merge patch and returns the updated object.
func (c *Client) Patch(ctx context.Context, res Resource, namespace, name string, patch []byte) (*unstructured.Unstructured, error) {
	if !res.Namespaced {
		namespace = ""
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	segs := append(resourcePath(res.GVR, namespace), name)
	raw, err := c.rest.Patch(types.MergePatchType).
		AbsPath(segs...).
		Body(patch).
		Do(pctx).Raw()
	if err != nil {
		return nil, fmt.Errorf("patch %s/%s: %w", res.Name(), name, err)
	}
	u := &unstructured.Unstructured{}
	if err := k8sjson.Unmarshal(raw, &u.Object); err != nil {
		return nil, fmt.Errorf("decode patched %s/%s: %w", res.Name(), name, err)
	}
	return u, nil
}

// Delete removes an object. A nil gracePeriod uses the server default.
func (c *Client) Delete(ctx context.Context, res Resource, namespace, name string, gracePeriod *int64) error {
	if !res.Namespaced {
		namespace = ""
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	segs := append(resourcePath(res.GVR, namespace), name)
	req := c.rest.Delete().AbsPath(segs...)
	if gracePeriod != nil {
		req = req.Param("gracePeriodSeconds", fmt.Sprint(*gracePeriod))
	}
	if err := req.Do(dctx).Error(); err != nil {
		return fmt.Errorf("delete %s/%s: %w", res.Name(), name, err)
	}
	return nil
}
