package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// StatusBar is the one-line footer: breadcrumbs on the left, state on the
// right.
type StatusBar struct {
	Context   string
	Namespace string
	// Crumbs are scope segments after the namespace, largest first.
	Crumbs []string
	// Count is free text such as "9 rows"; empty hides it.
	Count string
	// Forwards, when > 0, shows a port-forward indicator.
	Forwards int
	State    string
	Err      string
	// Notice is a transient success message, shown in place of the hint.
	Notice string
	// Hint is key help shown on the right when there is room and no error.
	Hint string
	// Back names where esc goes, e.g. "esc back to pods". Kept longer than
	// Hint when space is short.
	Back string
}

var (
	barStyle   = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))
	crumbStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("81")).Bold(true)
	sepStyle   = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("243"))
	liveStyle  = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("114"))
	warnStyle  = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("214"))
	errStyle   = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("203"))
)

// Render draws the bar at the given width.
func (s StatusBar) Render(width int) string {
	ns := s.Namespace
	if ns == "" {
		ns = "all"
	}
	sep := sepStyle.Render(" › ")
	left := crumbStyle.Render(s.Context) + sep + crumbStyle.Render(ns)
	for _, c := range s.Crumbs {
		left += sep + crumbStyle.Render(c)
	}

	count := ""
	if s.Count != "" {
		count = s.Count + " "
	}
	if s.Forwards > 0 {
		count = fmt.Sprintf("⇄%d  ", s.Forwards) + count
	}
	var right string
	switch {
	case s.Err != "":
		right = errStyle.Render(truncate(s.Err, width/2))
	case s.Notice != "":
		right = liveStyle.Render(truncate(s.Notice, width/2))
	case s.State == "live":
		right = barStyle.Render(count) + liveStyle.Render("● live")
	default:
		right = barStyle.Render(count) + warnStyle.Render("◌ "+s.State)
	}
	// Right side, most important last: count/state always, then Back, then
	// Hint. Never wrap: drop Hint, then Back, then truncate the left side.
	fits := func(extra string) bool {
		return width-lipgloss.Width(left)-lipgloss.Width(extra)-lipgloss.Width(right)-2 >= 1
	}
	if s.Err == "" && s.Notice == "" {
		back, hint := "", ""
		if s.Back != "" {
			back = sepStyle.Render(s.Back + "  ")
		}
		if s.Hint != "" {
			hint = sepStyle.Render(s.Hint + "  ")
		}
		switch {
		case fits(hint + back):
			right = hint + back + right
		case fits(back):
			right = back + right
		}
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		left = truncate(left, max(width-lipgloss.Width(right)-3, 4))
		gap = 1
	}
	line := " " + left + barStyle.Render(strings.Repeat(" ", gap)) + right + " "
	return barStyle.MaxWidth(width).Inline(true).Render(barStyle.Width(width).Render(line))
}

func truncate(s string, n int) string {
	if n < 4 || lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
