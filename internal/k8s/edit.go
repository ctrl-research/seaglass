package k8s

import (
	"context"

	"fmt"
	"time"

	k8sjson "k8s.io/apimachinery/pkg/util/json"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// ErrEditConflict means the object changed on the server since it was read;
// the edit was not applied.
type ErrEditConflict struct{ Detail string }

func (e ErrEditConflict) Error() string { return e.Detail }

// ErrEditInvalid means the edited object was rejected as invalid.
type ErrEditInvalid struct{ Detail string }

func (e ErrEditInvalid) Error() string { return e.Detail }

// Update replaces an object from edited YAML. The YAML must carry the
// resourceVersion it was read with; the server rejects a stale one as a
// conflict. Immutable-field or schema violations come back as invalid.
func (c *Client) Update(ctx context.Context, res Resource, namespace, name string, editedYAML []byte) (*unstructured.Unstructured, error) {
	raw, err := yaml.YAMLToJSON(editedYAML)
	if err != nil {
		return nil, ErrEditInvalid{Detail: "not valid YAML: " + err.Error()}
	}
	obj := &unstructured.Unstructured{}
	if err := k8sjson.Unmarshal(raw, &obj.Object); err != nil {
		return nil, ErrEditInvalid{Detail: "not a Kubernetes object: " + err.Error()}
	}
	if obj.GetName() != name {
		return nil, ErrEditInvalid{Detail: fmt.Sprintf("metadata.name changed from %q to %q; rename is not supported", name, obj.GetName())}
	}
	if !res.Namespaced {
		namespace = ""
	}

	uctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	segs := append(resourcePath(res.GVR, namespace), name)
	result, err := c.rest.Put().AbsPath(segs...).Body(raw).Do(uctx).Raw()
	if err != nil {
		switch {
		case apierrors.IsConflict(err):
			return nil, ErrEditConflict{Detail: apiErrorMessage(err)}
		case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
			return nil, ErrEditInvalid{Detail: apiErrorMessage(err)}
		default:
			return nil, fmt.Errorf("update %s/%s: %w", res.Name(), name, err)
		}
	}
	out := &unstructured.Unstructured{}
	if err := k8sjson.Unmarshal(result, &out.Object); err != nil {
		return nil, fmt.Errorf("decode updated %s/%s: %w", res.Name(), name, err)
	}
	return out, nil
}

// apiErrorMessage extracts the server's human message from a status error.
func apiErrorMessage(err error) string {
	if se, ok := err.(apierrors.APIStatus); ok {
		if msg := se.Status().Message; msg != "" {
			return msg
		}
	}
	return err.Error()
}

// EditableYAML renders an object for editing: managedFields removed, and
// the volatile status and metadata bookkeeping fields dropped so the edit
// is about spec. resourceVersion is kept so the update can detect a
// conflict.
func EditableYAML(u *unstructured.Unstructured) (string, error) {
	cp := u.DeepCopy()
	unstructured.RemoveNestedField(cp.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(cp.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(cp.Object, "metadata", "generation")
	unstructured.RemoveNestedField(cp.Object, "status")
	b, err := yaml.Marshal(cp.Object)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
