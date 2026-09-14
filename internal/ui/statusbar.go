package ui

import (
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
	State string
	Err   string
	// Hint is shown on the right when there is no error, e.g. key help.
	Hint string
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
	var right string
	switch {
	case s.Err != "":
		right = errStyle.Render(truncate(s.Err, width/2))
	case s.State == "live":
		right = barStyle.Render(count) + liveStyle.Render("● live")
	default:
		right = barStyle.Render(count) + warnStyle.Render("◌ "+s.State)
	}
	if s.Hint != "" && s.Err == "" {
		right = sepStyle.Render(s.Hint+"  ") + right
	}

	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	line := " " + left + barStyle.Render(strings.Repeat(" ", gap)) + right + " "
	return barStyle.Width(width).Render(line)
}

func truncate(s string, n int) string {
	if n < 4 || lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
