package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	confirmBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("214")).Padding(1, 3)
	confirmTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	confirmKeys  = lipgloss.NewStyle().Faint(true)
	confirmYes   = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
)

// Confirm renders a centered yes/no dialog over a blank body of the given
// size. The caller hides the underlying view while it is shown.
func Confirm(title, detail string, width, height int) string {
	body := confirmTitle.Render(title) + "\n" + detail + "\n\n" +
		confirmYes.Render("y") + confirmKeys.Render(" confirm    ") + lipgloss.NewStyle().Bold(true).Render("n") + confirmKeys.Render("/esc cancel")
	box := confirmBox.Render(body)
	if lipgloss.Width(box) > width {
		box = lipgloss.NewStyle().MaxWidth(width).Render(box)
	}
	boxH := lipgloss.Height(box)
	top := max((height-boxH)/2, 0)
	out := strings.Repeat("\n", top) + lipgloss.PlaceHorizontal(width, lipgloss.Center, box)
	return lipgloss.NewStyle().Height(height).MaxHeight(height).Render(out)
}
