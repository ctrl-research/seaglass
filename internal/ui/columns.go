// Package ui holds reusable, cluster-agnostic view helpers.
package ui

import (
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// cellPad is the horizontal padding the Bubbles table adds per cell (one
// space each side by default).
const cellPad = 2

// minColWidth is the narrowest a column may be squeezed before we give up
// and let it truncate.
const minColWidth = 6

// ColumnMode controls which optional (priority > 0) columns show.
type ColumnMode int

const (
	// ColumnsAuto shows optional columns while they fit.
	ColumnsAuto ColumnMode = iota
	// ColumnsWide shows every column, shrinking to fit.
	ColumnsWide
	// ColumnsNarrow shows only priority 0 columns.
	ColumnsNarrow
)

func (m ColumnMode) String() string {
	switch m {
	case ColumnsWide:
		return "wide"
	case ColumnsNarrow:
		return "narrow"
	}
	return "auto"
}

// Next cycles auto → wide → narrow → auto.
func (m ColumnMode) Next() ColumnMode { return (m + 1) % 3 }

// FitColumns chooses which server columns to show and how wide, given the
// terminal width and mode. Priority 0 columns are always included; in auto
// mode higher priorities are added in order while they fit. Returns the
// table columns and the indices into the source cells that each
// corresponds to.
func FitColumns(cols []k8s.Column, rows []k8s.Row, width int, mode ColumnMode) ([]table.Column, []int) {
	if len(cols) == 0 || width <= 0 {
		return nil, nil
	}
	natural := make([]int, len(cols))
	for i, c := range cols {
		natural[i] = lipgloss.Width(c.Name)
	}
	for _, r := range rows {
		for i := 0; i < len(cols) && i < len(r.Cells); i++ {
			if w := lipgloss.Width(r.Cells[i]); w > natural[i] {
				natural[i] = w
			}
		}
	}

	// Always-on columns.
	var idx []int
	used := 0
	for i, c := range cols {
		if c.Priority == 0 {
			idx = append(idx, i)
			used += natural[i] + cellPad
		}
	}
	// Optional columns by ascending priority, in server order within a tier.
	maxPri := int32(0)
	for _, c := range cols {
		if c.Priority > maxPri {
			maxPri = c.Priority
		}
	}
	for p := int32(1); p <= maxPri && mode != ColumnsNarrow; p++ {
		for i, c := range cols {
			if c.Priority != p {
				continue
			}
			if mode == ColumnsWide || used+natural[i]+cellPad <= width {
				idx = append(idx, i)
				used += natural[i] + cellPad
			}
		}
	}
	// Keep server order so the table reads like kubectl.
	sortInts(idx)

	widths := make([]int, len(idx))
	for k, i := range idx {
		widths[k] = natural[i]
	}
	// Shrink the widest columns until the required set fits.
	for used > width {
		widest := -1
		for k, w := range widths {
			if w > minColWidth && (widest < 0 || w > widths[widest]) {
				widest = k
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
		used--
	}
	// Give leftover space to the last column so the selection bar spans
	// the terminal.
	if len(widths) > 0 && used < width {
		widths[len(widths)-1] += width - used
	}

	out := make([]table.Column, len(idx))
	for k, i := range idx {
		out[k] = table.Column{Title: cols[i].Name, Width: widths[k]}
	}
	return out, idx
}

// ProjectRows converts k8s rows to table rows using the chosen column indices.
func ProjectRows(rows []k8s.Row, idx []int) []table.Row {
	out := make([]table.Row, len(rows))
	for r, row := range rows {
		cells := make(table.Row, len(idx))
		for k, i := range idx {
			if i < len(row.Cells) {
				cells[k] = row.Cells[i]
			}
		}
		out[r] = cells
	}
	return out
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// WarnStyle colors a warning event row.
var warnRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

// ProjectRowsWarn is like ProjectRows but colors every cell of a row whose
// cell at warnCol equals "Warning" (case-insensitive). warnCol < 0 disables
// it. Coloring is applied to the cell strings so the table renders it for
// non-selected rows.
func ProjectRowsWarn(rows []k8s.Row, idx []int, warnCol int) []table.Row {
	out := make([]table.Row, len(rows))
	for r, row := range rows {
		warn := warnCol >= 0 && warnCol < len(row.Cells) && strings.EqualFold(row.Cells[warnCol], "Warning")
		cells := make(table.Row, len(idx))
		for k, i := range idx {
			val := ""
			if i < len(row.Cells) {
				val = row.Cells[i]
			}
			if warn {
				val = warnRowStyle.Render(val)
			}
			cells[k] = val
		}
		out[r] = cells
	}
	return out
}

// Severity ranks a status value for coloring.
type Severity int

const (
	// SevNone leaves the value unstyled.
	SevNone Severity = iota
	// SevOK is a healthy/steady state.
	SevOK
	// SevWarn is transient or not-yet-ready.
	SevWarn
	// SevError is a failure that needs attention.
	SevError
)

var (
	sevOKStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	sevWarnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	sevErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
)

// errorStatuses and warnStatuses classify common pod/container status
// values seen in the Status column.
var errorStatuses = map[string]bool{
	"CrashLoopBackOff": true, "Error": true, "ImagePullBackOff": true,
	"ErrImagePull": true, "OOMKilled": true, "Evicted": true, "Failed": true,
	"CreateContainerError": true, "CreateContainerConfigError": true,
	"RunContainerError": true, "InvalidImageName": true, "ErrImageNeverPull": true,
	"Unschedulable": true, "NodeLost": true, "DeadlineExceeded": true,
}

var warnStatuses = map[string]bool{
	"Pending": true, "ContainerCreating": true, "PodInitializing": true,
	"Terminating": true, "NotReady": true, "Init": true, "SchedulingGated": true,
}

// StatusSeverity classifies a Status-column value. "Init:0/2" and
// "Init:Error" style by their prefix/suffix.
func StatusSeverity(v string) Severity {
	switch {
	case v == "Running" || v == "Completed" || v == "Succeeded":
		return SevOK
	case errorStatuses[v]:
		return SevError
	case warnStatuses[v]:
		return SevWarn
	case strings.HasPrefix(v, "Init:"):
		if strings.Contains(v, "Error") || strings.Contains(v, "CrashLoop") {
			return SevError
		}
		return SevWarn
	default:
		return SevNone
	}
}

// StyleStatus colors a value by its severity.
func StyleStatus(v string) string {
	switch StatusSeverity(v) {
	case SevOK:
		return sevOKStyle.Render(v)
	case SevWarn:
		return sevWarnStyle.Render(v)
	case SevError:
		return sevErrorStyle.Render(v)
	default:
		return v
	}
}

// ProjectRowsStatus is like ProjectRows but colors the cell at statusCol by
// its severity. statusCol < 0 disables it.
func ProjectRowsStatus(rows []k8s.Row, idx []int, statusCol int) []table.Row {
	out := make([]table.Row, len(rows))
	for r, row := range rows {
		cells := make(table.Row, len(idx))
		for k, i := range idx {
			val := ""
			if i < len(row.Cells) {
				val = row.Cells[i]
			}
			if i == statusCol {
				val = StyleStatus(val)
			}
			cells[k] = val
		}
		out[r] = cells
	}
	return out
}

// StatusColumn returns the index of a "Status" column, or -1.
func StatusColumn(cols []k8s.Column) int {
	for i, c := range cols {
		if strings.EqualFold(c.Name, "Status") {
			return i
		}
	}
	return -1
}
