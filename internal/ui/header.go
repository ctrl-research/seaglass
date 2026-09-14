package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// logo is the wordmark, six lines of figlet-style ASCII.
var logo = []string{
	"  ____                   _               ",
	" / ___|  ___  __ _  __ _| | __ _ ___ ___ ",
	" \\___ \\ / _ \\/ _` |/ _` | |/ _` / __/ __|",
	"  ___) |  __/ (_| | (_| | | (_| \\__ \\__ \\",
	" |____/ \\___|\\__,_|\\__, |_|\\__,_|___/___/",
	"                   |___/                 ",
}

const logoWidth = 41

var (
	logoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("79")).Bold(true)
	hdrKeyStyle   = lipgloss.NewStyle().Faint(true)
	hdrValStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	hdrRuleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	headerMinRows = 22 // terminal height below which the header is hidden
	headerMinCols = 60
)

// Field is one key/value pair in the header info block.
type Field struct{ Key, Value string }

// Header is the top-of-screen block: logo on the left, a column of fields
// beside it, and a single horizontal rule beneath.
type Header struct {
	// Fields are shown one per logo line, up to six.
	Fields []Field
}

// HeaderHeight is the number of lines Header.Render produces when shown.
const HeaderHeight = 7

// Visible reports whether the header fits the terminal.
func (h Header) Visible(width, height int) bool {
	return height >= headerMinRows && width >= headerMinCols
}

// Render draws the header at the given width. Call Visible first. Fields
// are dropped when there is no room beside the logo.
func (h Header) Render(width int) string {
	showFields := width-logoWidth-2 >= 24
	lines := make([]string, len(logo))
	for i, l := range logo {
		line := logoStyle.Render(l)
		if showFields && i < len(h.Fields) {
			line += "  " + renderField(h.Fields[i], 0)
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
