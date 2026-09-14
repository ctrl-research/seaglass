package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// HelpSection is a titled group of bindings.
type HelpSection struct {
	Title    string
	Bindings []key.Binding
}

var (
	helpTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	helpKeyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	helpDescStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	helpFootStyle  = lipgloss.NewStyle().Faint(true)
)

// RenderHelp lays sections out in columns that fit the width, each column
// about 40 cells, and pads to height.
func RenderHelp(sections []HelpSection, width, height int) string {
	const colW = 40
	ncols := max(1, min(len(sections), (width-2)/colW))
	cols := make([][]string, ncols)
	for i, s := range sections {
		cols[i%ncols] = append(cols[i%ncols], renderSection(s, colW-2), "")
	}
	blocks := make([]string, ncols)
	for i, lines := range cols {
		blocks[i] = lipgloss.NewStyle().Width(colW).Render(strings.Join(lines, "\n"))
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
	foot := helpFootStyle.Render("press ? or esc to close")
	out := lipgloss.NewStyle().Padding(1, 2).Render(body + "\n" + foot)
	return lipgloss.NewStyle().Height(height).MaxHeight(height).MaxWidth(width).Render(out)
}

func renderSection(s HelpSection, width int) string {
	keyW := 0
	for _, b := range s.Bindings {
		if !b.Enabled() {
			continue
		}
		keyW = max(keyW, lipgloss.Width(b.Help().Key))
	}
	var sb strings.Builder
	sb.WriteString(helpTitleStyle.Render(s.Title) + "\n")
	for _, b := range s.Bindings {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		line := fmt.Sprintf("%s  %s", helpKeyStyle.Render(fmt.Sprintf("%-*s", keyW, h.Key)), helpDescStyle.Render(h.Desc))
		sb.WriteString(lipgloss.NewStyle().MaxWidth(width).Render(line) + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
