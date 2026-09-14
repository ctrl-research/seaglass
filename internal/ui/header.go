package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// logo is a three-line wordmark in box-drawing characters.
var logo = []string{
	"┌─┐┌─┐┌─┐┌─┐┬  ┌─┐┌─┐┌─┐",
	"└─┐├┤ ├─┤│ ┬│  ├─┤└─┐└─┐",
	"└─┘└─┘┴ ┴└─┘┴─┘┴ ┴└─┘└─┘",
}

const logoWidth = 24

var (
	logoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("79")).Bold(true)
	hdrKeyStyle   = lipgloss.NewStyle().Faint(true)
	hdrValStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	hdrRuleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	headerMinRows = 18 // terminal height below which the header is hidden
	headerMinCols = 48
)

// Field is one key/value pair in the header info block.
type Field struct{ Key, Value string }

// Header is the top-of-screen block: logo on the left, two columns of
// fields beside it, and a single horizontal rule beneath.
type Header struct {
	// Left column fields (up to 3) and right column fields (up to 3).
	Left, Right []Field
}

// HeaderHeight is the number of lines Header.Render produces when shown.
const HeaderHeight = 4

// Visible reports whether the header fits the terminal.
func (h Header) Visible(width, height int) bool {
	return height >= headerMinRows && width >= headerMinCols
}

// Render draws the header at the given width. Call Visible first.
func (h Header) Render(width int) string {
	avail := width - logoWidth - 2
	leftW := 0
	for _, f := range h.Left {
		leftW = max(leftW, lipgloss.Width(f.Key)+2+lipgloss.Width(f.Value))
	}
	showRight := len(h.Right) > 0 && avail-leftW-3 >= 20

	lines := make([]string, 3)
	for i := 0; i < 3; i++ {
		line := logoStyle.Render(logo[i]) + "  "
		if i < len(h.Left) {
			line += renderField(h.Left[i], leftW)
		} else {
			line += strings.Repeat(" ", leftW)
		}
		if showRight && i < len(h.Right) {
			line += "   " + renderField(h.Right[i], 0)
		}
		lines[i] = lipgloss.NewStyle().MaxWidth(width).Render(line)
	}
	rule := hdrRuleStyle.Render(strings.Repeat("─", width))
	return strings.Join(lines, "\n") + "\n" + rule
}

func renderField(f Field, padTo int) string {
	s := hdrKeyStyle.Render(f.Key+":") + " " + hdrValStyle.Render(f.Value)
	if pad := padTo - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}
