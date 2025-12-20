// Package ui provides terminal styling for beads CLI output.
// Uses the Ayu color theme with adaptive light/dark mode support.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Ayu theme color palette
// Dark: https://terminalcolors.com/themes/ayu/dark/
// Light: https://terminalcolors.com/themes/ayu/light/
var (
	// Semantic status colors (Ayu theme - adaptive light/dark)
	ColorPass = lipgloss.AdaptiveColor{
		Light: "#86b300", // ayu light bright green
		Dark:  "#c2d94c", // ayu dark bright green
	}
	ColorWarn = lipgloss.AdaptiveColor{
		Light: "#f2ae49", // ayu light bright yellow
		Dark:  "#ffb454", // ayu dark bright yellow
	}
	ColorFail = lipgloss.AdaptiveColor{
		Light: "#f07171", // ayu light bright red
		Dark:  "#f07178", // ayu dark bright red
	}
	ColorMuted = lipgloss.AdaptiveColor{
		Light: "#828c99", // ayu light muted
		Dark:  "#6c7680", // ayu dark muted
	}
	ColorAccent = lipgloss.AdaptiveColor{
		Light: "#399ee6", // ayu light bright blue
		Dark:  "#59c2ff", // ayu dark bright blue
	}
)

// Status styles - consistent across all commands
var (
	PassStyle   = lipgloss.NewStyle().Foreground(ColorPass)
	WarnStyle   = lipgloss.NewStyle().Foreground(ColorWarn)
	FailStyle   = lipgloss.NewStyle().Foreground(ColorFail)
	MutedStyle  = lipgloss.NewStyle().Foreground(ColorMuted)
	AccentStyle = lipgloss.NewStyle().Foreground(ColorAccent)
)

// CategoryStyle for section headers - bold with accent color
var CategoryStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

// Status icons - consistent semantic indicators
const (
	IconPass = "✓"
	IconWarn = "⚠"
	IconFail = "✗"
	IconSkip = "-"
	IconInfo = "ℹ"
)

// Tree characters for hierarchical display
const (
	TreeChild  = "⎿ "  // child indicator
	TreeLast   = "└─ " // last child / detail line
	TreeIndent = "  "  // 2-space indent per level
)

// Separators
const (
	SeparatorLight = "──────────────────────────────────────────"
	SeparatorHeavy = "══════════════════════════════════════════"
)

// RenderPass renders text with pass (green) styling
func RenderPass(s string) string {
	return PassStyle.Render(s)
}

// RenderWarn renders text with warning (yellow) styling
func RenderWarn(s string) string {
	return WarnStyle.Render(s)
}

// RenderFail renders text with fail (red) styling
func RenderFail(s string) string {
	return FailStyle.Render(s)
}

// RenderMuted renders text with muted (gray) styling
func RenderMuted(s string) string {
	return MutedStyle.Render(s)
}

// RenderAccent renders text with accent (blue) styling
func RenderAccent(s string) string {
	return AccentStyle.Render(s)
}

// RenderCategory renders a category header in uppercase with accent color
func RenderCategory(s string) string {
	return CategoryStyle.Render(strings.ToUpper(s))
}

// RenderSeparator renders the light separator line in muted color
func RenderSeparator() string {
	return MutedStyle.Render(SeparatorLight)
}

// RenderPassIcon renders the pass icon with styling
func RenderPassIcon() string {
	return PassStyle.Render(IconPass)
}

// RenderWarnIcon renders the warning icon with styling
func RenderWarnIcon() string {
	return WarnStyle.Render(IconWarn)
}

// RenderFailIcon renders the fail icon with styling
func RenderFailIcon() string {
	return FailStyle.Render(IconFail)
}

// RenderSkipIcon renders the skip icon with styling
func RenderSkipIcon() string {
	return MutedStyle.Render(IconSkip)
}

// RenderInfoIcon renders the info icon with styling
func RenderInfoIcon() string {
	return AccentStyle.Render(IconInfo)
}
