package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func visibleWidth(s string) int { return lipgloss.Width(s) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }
