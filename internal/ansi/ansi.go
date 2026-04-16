// Package ansi provides lightweight ANSI terminal styling.
// Replaces charm.land/lipgloss for beads' needs: foreground color, bold, render.
package ansi

import (
	"fmt"
	"strconv"
)

// Color represents a hex color string like "#ff0000".
// An empty string means no color (passthrough).
type Color string

// NoColor is the zero value — renders text without any color escape codes.
const NoColor Color = ""

// Style holds foreground color and bold state for terminal rendering.
type Style struct {
	fg   Color
	bold bool
}

// NewStyle returns an empty style that renders text unchanged.
func NewStyle() Style { return Style{} }

// Foreground returns a copy of the style with the given foreground color.
func (s Style) Foreground(c Color) Style { s.fg = c; return s }

// Bold returns a copy of the style with bold enabled or disabled.
func (s Style) Bold(b bool) Style { s.bold = b; return s }

// Render wraps text with ANSI escape sequences for this style.
// Returns text unchanged if no color or bold is set.
func (s Style) Render(text string) string {
	if s.fg == "" && !s.bold {
		return text
	}
	var prefix string
	if s.bold {
		prefix += "\x1b[1m"
	}
	if s.fg != "" {
		r, g, b := hexToRGB(s.fg)
		prefix += fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	}
	return prefix + text + "\x1b[0m"
}

// LightDark returns the light color when isDark is false, dark color when true.
// This mimics lipgloss.LightDark for adaptive theming.
func LightDark(isDark bool, light, dark Color) Color {
	if isDark {
		return dark
	}
	return light
}

// hexToRGB parses a Color like "#ff8800" into r, g, b components.
func hexToRGB(c Color) (uint8, uint8, uint8) {
	s := string(c)
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	if len(s) != 6 {
		return 0, 0, 0
	}
	r, _ := strconv.ParseUint(s[0:2], 16, 8)
	g, _ := strconv.ParseUint(s[2:4], 16, 8)
	b, _ := strconv.ParseUint(s[4:6], 16, 8)
	return uint8(r), uint8(g), uint8(b)
}
