package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
)

var (
	descKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	descSectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	descDimStyle     = lipgloss.NewStyle().Faint(true)
	descTrueStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	descFalseStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// Describe renders a generic, type-agnostic summary of any object: identity
// and metadata, scalar status fields, conditions, and containers when the
// spec has them. It is the one-code-path counterpart to kubectl describe.
func Describe(u *unstructured.Unstructured, now time.Time) string {
	var b strings.Builder
	kv := func(k, v string) {
		fmt.Fprintf(&b, "%s %s\n", descKeyStyle.Render(fmt.Sprintf("%-13s", k+":")), v)
	}

	kv("Kind", u.GetKind()+descDimStyle.Render("  "+u.GetAPIVersion()))
	kv("Name", u.GetName())
	if ns := u.GetNamespace(); ns != "" {
		kv("Namespace", ns)
	}
	if ts := u.GetCreationTimestamp(); !ts.IsZero() {
		kv("Created", ts.Local().Format("2006-01-02 15:04:05")+descDimStyle.Render("  "+duration.HumanDuration(now.Sub(ts.Time))+" ago"))
	}
	if dt := u.GetDeletionTimestamp(); dt != nil {
		kv("Deleting", descFalseStyle.Render("since "+dt.Local().Format("15:04:05")))
	}
	kv("Labels", renderMap(u.GetLabels()))
	kv("Annotations", renderMap(u.GetAnnotations()))
	if owners := u.GetOwnerReferences(); len(owners) > 0 {
		parts := make([]string, len(owners))
		for i, o := range owners {
			parts[i] = o.Kind + "/" + o.Name
		}
		kv("Owner", strings.Join(parts, ", "))
	}
	kv("UID", descDimStyle.Render(string(u.GetUID())))

	if status, ok := u.Object["status"].(map[string]any); ok {
		if scalars := scalarFields(status); len(scalars) > 0 {
			b.WriteString("\n" + descSectionStyle.Render("Status") + "\n")
			for _, s := range scalars {
				fmt.Fprintf(&b, "  %s %s\n", descKeyStyle.Render(fmt.Sprintf("%-20s", s.key+":")), s.val)
			}
		}
		if conds, ok := status["conditions"].([]any); ok && len(conds) > 0 {
			b.WriteString("\n" + descSectionStyle.Render("Conditions") + "\n")
			b.WriteString(renderConditions(conds))
		}
	}

	if spec, ok := u.Object["spec"].(map[string]any); ok {
		if containers, ok := spec["containers"].([]any); ok && len(containers) > 0 {
			b.WriteString("\n" + descSectionStyle.Render("Containers") + "\n")
			b.WriteString(renderContainers(containers))
		}
	}
	if status, ok := u.Object["status"].(map[string]any); ok {
		if cs, ok := status["containerStatuses"].([]any); ok && len(cs) > 0 {
			b.WriteString("\n" + descSectionStyle.Render("Container status") + "\n")
			b.WriteString(renderContainerStatuses(cs))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderMap(m map[string]string) string {
	if len(m) == 0 {
		return descDimStyle.Render("<none>")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		v := m[k]
		if len(v) > 100 {
			v = v[:97] + "..."
		}
		lines[i] = k + "=" + v
	}
	return strings.Join(lines, "\n"+strings.Repeat(" ", 14))
}

type scalar struct{ key, val string }

// scalarFields returns the top-level non-collection fields of a map,
// sorted by key. Nested maps and lists are skipped: the YAML view has them.
func scalarFields(m map[string]any) []scalar {
	var out []scalar
	for k, v := range m {
		switch x := v.(type) {
		case map[string]any, []any:
			continue
		case nil:
			out = append(out, scalar{k, descDimStyle.Render("null")})
		default:
			out = append(out, scalar{k, fmt.Sprint(x)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func renderConditions(conds []any) string {
	rows := [][]string{{"TYPE", "STATUS", "REASON", "MESSAGE"}}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{str(m["type"]), str(m["status"]), str(m["reason"]), str(m["message"])})
	}
	return renderTable(rows, func(row, col int, s string) string {
		if row == 0 {
			return descDimStyle.Render(s)
		}
		if col == 1 {
			switch s {
			case "True":
				return descTrueStyle.Render(s)
			case "False":
				return descFalseStyle.Render(s)
			}
		}
		return s
	})
}

func renderContainers(containers []any) string {
	rows := [][]string{{"NAME", "IMAGE", "PORTS"}}
	for _, c := range containers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		var ports []string
		if ps, ok := m["ports"].([]any); ok {
			for _, p := range ps {
				if pm, ok := p.(map[string]any); ok {
					ports = append(ports, str(pm["containerPort"])+"/"+strings.ToLower(str(pm["protocol"])))
				}
			}
		}
		rows = append(rows, []string{str(m["name"]), str(m["image"]), strings.Join(ports, ",")})
	}
	return renderTable(rows, func(row, _ int, s string) string {
		if row == 0 {
			return descDimStyle.Render(s)
		}
		return s
	})
}

// renderTable pads columns to their widest cell. style may decorate cells;
// it runs after padding so ANSI codes do not skew alignment.
func renderTable(rows [][]string, style func(row, col int, s string) string) string {
	if len(rows) == 0 {
		return ""
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	var b strings.Builder
	for ri, r := range rows {
		b.WriteString("  ")
		for ci, c := range r {
			cell := c
			if ci < len(r)-1 {
				cell = fmt.Sprintf("%-*s", widths[ci], c)
			}
			b.WriteString(style(ri, ci, cell))
			if ci < len(r)-1 {
				b.WriteString("  ")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case float64:
		return fmt.Sprintf("%v", int64(x))
	default:
		return fmt.Sprint(x)
	}
}

// renderContainerStatuses summarizes status.containerStatuses, highlighting
// problem states (CrashLoopBackOff, OOMKilled, ImagePullBackOff, …) and
// restart counts.
func renderContainerStatuses(cs []any) string {
	rows := [][]string{{"NAME", "READY", "RESTARTS", "STATE", "REASON"}}
	var severities []Severity
	severities = append(severities, SevNone) // header
	for _, c := range cs {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ready := "false"
		if r, ok := m["ready"].(bool); ok && r {
			ready = "true"
		}
		state, reason := containerState(m)
		rows = append(rows, []string{str(m["name"]), ready, str(m["restartCount"]), state, reason})
		severities = append(severities, StatusSeverity(reason))
	}
	return renderTable(rows, func(row, col int, s string) string {
		if row == 0 {
			return descDimStyle.Render(s)
		}
		// Color the STATE and REASON cells by the row's severity.
		if col >= 3 && row < len(severities) {
			switch severities[row] {
			case SevError:
				return descFalseStyle.Render(s)
			case SevWarn:
				return descKeyStyle.Render(s)
			}
		}
		if col == 1 && s == "false " || col == 1 && s == "false" {
			return descFalseStyle.Render(s)
		}
		return s
	})
}

// containerState returns a container's current state and its reason. It also
// reports OOMKilled from a last-terminated state, which is the common way an
// OOM shows up on a now-restarting container.
func containerState(m map[string]any) (state, reason string) {
	st, _ := m["state"].(map[string]any)
	switch {
	case has(st, "running"):
		state = "Running"
	case has(st, "waiting"):
		state = "Waiting"
		if w, ok := st["waiting"].(map[string]any); ok {
			reason = str(w["reason"])
		}
	case has(st, "terminated"):
		state = "Terminated"
		if term, ok := st["terminated"].(map[string]any); ok {
			reason = str(term["reason"])
		}
	}
	// Surface an OOMKilled from the previous run when currently waiting.
	if reason == "" || reason == "CrashLoopBackOff" {
		if last, ok := m["lastState"].(map[string]any); ok {
			if term, ok := last["terminated"].(map[string]any); ok {
				if r := str(term["reason"]); r == "OOMKilled" {
					reason = "OOMKilled"
					if state == "Waiting" {
						reason = "CrashLoopBackOff (OOMKilled)"
					}
				}
			}
		}
	}
	return state, reason
}

func has(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	_, ok := m[key]
	return ok
}
