package app

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ctrl-research/seaglass/internal/config"
	"github.com/ctrl-research/seaglass/internal/k8s"
)

func unstructuredNestedFieldNoCopy(obj *k8sUnstructured, fields ...string) (any, bool, error) {
	return unstructured.NestedFieldNoCopy(obj.Object, fields...)
}

// badgesFor returns the badge rules whose match applies to a resource.
func badgesFor(res k8s.Resource, badges []config.BadgeRule) []config.BadgeRule {
	var out []config.BadgeRule
	for _, b := range badges {
		if b.Match.Matches(res.GVR.Group, res.GVR.Resource, res.Kind) {
			out = append(out, b)
		}
	}
	return out
}

// rowBadge is the first badge that matches a row, if any.
type rowBadge struct {
	style string
	tag   string
}

// evalBadge reports the first matching badge for a row's object. Rows
// without a full object (no includeObject) cannot be evaluated.
func evalBadge(row k8s.Row, badges []config.BadgeRule) (rowBadge, bool) {
	if row.Object == nil {
		return rowBadge{}, false
	}
	for _, b := range badges {
		if predicateHolds(row, b.When) {
			return rowBadge{style: b.Style, tag: b.Tag}, true
		}
	}
	return rowBadge{}, false
}

// predicateHolds evaluates a badge predicate against a row's object.
func predicateHolds(row k8s.Row, p config.Predicate) bool {
	obj := row.Object
	if p.Condition != "" {
		return conditionStatus(obj, p.Condition) == p.Status
	}
	if p.Field != "" {
		got, _, _ := nestedString(obj, p.Field)
		if got == "" {
			// Booleans and numbers: compare their string form.
			got = nestedScalarString(obj, p.Field)
		}
		return got == p.Equals
	}
	return false
}

// nestedScalarString reads a bool/number field at a dotted path as text.
func nestedScalarString(obj *k8sUnstructured, path string) string {
	segs := splitPath(path)
	v, found, _ := unstructuredNestedFieldNoCopy(obj, segs...)
	if !found {
		return ""
	}
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return itoaInt64(x)
	case float64:
		return itoaInt64(int64(x))
	default:
		return ""
	}
}

func itoaInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// decorateBadges appends a "(tag)" to the primary cell of badged rows and
// returns the per-row badge (nil when none). It returns display rows whose
// widths include the tags, so column fitting accounts for them.
func decorateBadges(rows []k8s.Row, badges []config.BadgeRule) (display []k8s.Row, rowBadges []*rowBadge) {
	display = make([]k8s.Row, len(rows))
	rowBadges = make([]*rowBadge, len(rows))
	for i, r := range rows {
		display[i] = r
		if b, ok := evalBadge(r, badges); ok {
			rb := b
			rowBadges[i] = &rb
			if b.tag != "" && len(r.Cells) > 0 {
				cells := make([]string, len(r.Cells))
				copy(cells, r.Cells)
				cells[0] = strings.TrimRight(cells[0], " ") + " (" + b.tag + ")"
				display[i].Cells = cells
			}
		}
	}
	return display, rowBadges
}
