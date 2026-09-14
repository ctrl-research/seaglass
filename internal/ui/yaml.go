package ui

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	yamlKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	yamlNumStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	yamlBoolStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("176"))
	yamlCommentStyle = lipgloss.NewStyle().Faint(true)
	yamlDashStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	// indent, optional list dash, key, colon, rest
	yamlKeyRE   = regexp.MustCompile(`^(\s*)(- )?([^\s:#'"][^:]*?|'[^']*'|"[^"]*"):(\s.*|$)`)
	yamlNumRE   = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	yamlConstRE = regexp.MustCompile(`^(true|false|null|~|yes|no)$`)
)

// ColorizeYAML adds ANSI colors to YAML text line by line. It is a
// heuristic highlighter, not a parser: keys, numbers, booleans/null, list
// dashes, and comments get colors; everything else stays as is.
func ColorizeYAML(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		lines[i] = colorizeLine(line)
	}
	return strings.Join(lines, "\n")
}

func colorizeLine(line string) string {
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "#") {
		return yamlCommentStyle.Render(line)
	}
	if m := yamlKeyRE.FindStringSubmatch(line); m != nil {
		indent, dash, key, rest := m[1], m[2], m[3], m[4]
		out := indent
		if dash != "" {
			out += yamlDashStyle.Render("-") + " "
		}
		out += yamlKeyStyle.Render(key) + ":"
		out += colorizeValue(rest)
		return out
	}
	if strings.HasPrefix(trim, "- ") {
		idx := strings.Index(line, "- ")
		return line[:idx] + yamlDashStyle.Render("-") + " " + colorizeValue(line[idx+2:])
	}
	return line
}

func colorizeValue(rest string) string {
	if rest == "" {
		return ""
	}
	lead := len(rest) - len(strings.TrimLeft(rest, " "))
	val := strings.TrimSpace(rest)
	switch {
	case yamlNumRE.MatchString(val):
		return rest[:lead] + yamlNumStyle.Render(val)
	case yamlConstRE.MatchString(val):
		return rest[:lead] + yamlBoolStyle.Render(val)
	}
	return rest
}
