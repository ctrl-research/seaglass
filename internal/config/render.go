package config

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// RenderData is the context available to action templates: the object's
// fields (so {{.spec.replicas}} works), the prompt input, and now().
type RenderData struct {
	obj   map[string]any
	input string
	now   time.Time
}

// NewRenderData builds the template context.
func NewRenderData(obj map[string]any, input string, now time.Time) RenderData {
	return RenderData{obj: obj, input: input, now: now}
}

// RenderPatch renders every string leaf of a patch template, returning a
// new patch. Leaves that render to an integer or boolean are coerced to
// that type so numeric fields (spec.replicas) and booleans (spec.suspend)
// produce valid merge patches; everything else stays a string.
func RenderPatch(patch map[string]any, data RenderData) (map[string]any, error) {
	v, err := renderValue(patch, data)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	return m, nil
}

func renderValue(v any, data RenderData) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			rv, err := renderValue(val, data)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			rv, err := renderValue(val, data)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	case string:
		return renderString(x, data)
	default:
		return v, nil
	}
}

// renderString renders one template string and coerces int/bool results.
func renderString(s string, data RenderData) (any, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	out, err := renderRaw(s, data)
	if err != nil {
		return nil, err
	}
	if n, err := strconv.ParseInt(out, 10, 64); err == nil {
		return n, nil
	}
	if out == "true" {
		return true, nil
	}
	if out == "false" {
		return false, nil
	}
	return out, nil
}

// RenderString renders a template to a string without type coercion, for
// input defaults and wait predicates.
func RenderString(s string, data RenderData) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	return renderRaw(s, data)
}

func renderRaw(s string, data RenderData) (string, error) {
	t, err := template.New("v").Funcs(template.FuncMap{
		"now": func() string { return data.now.UTC().Format(time.RFC3339) },
	}).Option("missingkey=zero").Parse(s)
	if err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	ctx := map[string]any{"input": data.input}
	for k, v := range data.obj {
		ctx[k] = v
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	return buf.String(), nil
}
