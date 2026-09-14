package ui

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

var (
	// Kubernetes human durations: 52s, 142m, 5h30m, 2d19h, 3y12d.
	durationRE = regexp.MustCompile(`^(?:(\d+)y)?(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`)
	// Leading number, e.g. "3", "1 (142m ago)", "2.5", "-1".
	leadingNumRE = regexp.MustCompile(`^-?\d+(?:\.\d+)?`)
)

// Compare orders two cell strings the way a human expects: durations by
// length, numbers by value (including a leading number such as restarts
// "1 (142m ago)"), "<none>" and empty first, everything else
// case-insensitively.
func Compare(a, b string) int {
	ea, eb := a == "" || a == "<none>", b == "" || b == "<none>"
	switch {
	case ea && eb:
		return 0
	case ea:
		return -1
	case eb:
		return 1
	}
	if da, ok := parseHumanDuration(a); ok {
		if db, ok := parseHumanDuration(b); ok {
			return cmpInt(da, db)
		}
	}
	if na, ok := leadingNumber(a); ok {
		if nb, ok := leadingNumber(b); ok && na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
	}
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func parseHumanDuration(s string) (int64, bool) {
	if s == "" || !strings.ContainsAny(s, "ydhms") {
		return 0, false
	}
	m := durationRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	mult := []int64{365 * 86400, 86400, 3600, 60, 1}
	var total int64
	any := false
	for i, part := range m[1:] {
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return 0, false
		}
		total += n * mult[i]
		any = true
	}
	return total, any
}

func leadingNumber(s string) (float64, bool) {
	m := leadingNumRE.FindString(s)
	if m == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(m, 64)
	return f, err == nil
}

// SortRows orders rows by the cell at col, stably, descending when desc.
// Rows lacking the column sort first.
func SortRows(rows []k8s.Row, col int, desc bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		var a, b string
		if col < len(rows[i].Cells) {
			a = rows[i].Cells[col]
		}
		if col < len(rows[j].Cells) {
			b = rows[j].Cells[col]
		}
		c := Compare(a, b)
		if desc {
			return c > 0
		}
		return c < 0
	})
}
