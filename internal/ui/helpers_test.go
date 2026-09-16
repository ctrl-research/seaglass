package ui

import (
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

func visibleWidth(s string) int { return lipgloss.Width(s) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func timeNow() time.Time { return time.Now() }
