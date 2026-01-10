// Package ui provides terminal styling for beads CLI output.
// Uses the Ayu color theme with adaptive light/dark mode support.
package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func init() {
	if !ShouldUseColor() {
		// Disable colors when not appropriate (non-TTY, NO_COLOR, etc.)
		lipgloss.SetColorProfile(termenv.Ascii)
	} else {
		// Use TrueColor for distinct priority/status colors in modern terminals
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
}

// IsAgentMode returns true if the CLI is running in agent-optimized mode.
// This is triggered by:
//   - BD_AGENT_MODE=1 environment variable (explicit)
//   - CLAUDE_CODE environment variable (auto-detect Claude Code)
//
// Agent mode provides ultra-compact output optimized for LLM context windows.
func IsAgentMode() bool {
	if os.Getenv("BD_AGENT_MODE") == "1" {
		return true
	}
	// Auto-detect Claude Code environment
	if os.Getenv("CLAUDE_CODE") != "" {
		return true
	}
	return false
}

// Ayu theme color palette
// Dark: https://terminalcolors.com/themes/ayu/dark/
// Light: https://terminalcolors.com/themes/ayu/light/
// Source: https://github.com/ayu-theme/ayu-colors
var (
	// Core semantic colors (Ayu theme - adaptive light/dark)
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

	// === Workflow Status Colors ===
	// Only actionable states get color - open/closed match standard text
	ColorStatusOpen = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
	ColorStatusInProgress = lipgloss.AdaptiveColor{
		Light: "#f2ae49", // yellow - active work, very visible
		Dark:  "#ffb454",
	}
	ColorStatusClosed = lipgloss.AdaptiveColor{
		Light: "#9099a1", // slightly dimmed - visually shows "done"
		Dark:  "#8090a0",
	}
	ColorStatusBlocked = lipgloss.AdaptiveColor{
		Light: "#f07171", // red - needs attention
		Dark:  "#f26d78",
	}
	ColorStatusPinned = lipgloss.AdaptiveColor{
		Light: "#d2a6ff", // purple - special/elevated
		Dark:  "#d2a6ff",
	}
	ColorStatusHooked = lipgloss.AdaptiveColor{
		Light: "#59c2ff", // cyan - actively worked by agent (GUPP)
		Dark:  "#59c2ff",
	}

	// === Priority Colors ===
	// Only P0/P1 get color - they need attention
	// P2/P3/P4 are neutral (medium/low/backlog don't need visual urgency)
	ColorPriorityP0 = lipgloss.AdaptiveColor{
		Light: "#f07171", // bright red - critical, demands attention
		Dark:  "#f07178",
	}
	ColorPriorityP1 = lipgloss.AdaptiveColor{
		Light: "#ff8f40", // orange - high priority, needs attention soon
		Dark:  "#ff8f40",
	}
	ColorPriorityP2 = lipgloss.AdaptiveColor{
		Light: "#e6b450", // muted gold - medium priority, visible but calm
		Dark:  "#e6b450",
	}
	ColorPriorityP3 = lipgloss.AdaptiveColor{
		Light: "", // neutral - low priority
		Dark:  "",
	}
	ColorPriorityP4 = lipgloss.AdaptiveColor{
		Light: "", // neutral - backlog
		Dark:  "",
	}

	// === Issue Type Colors ===
	// Bugs and epics get color - they need attention
	// All other types use standard text
	ColorTypeBug = lipgloss.AdaptiveColor{
		Light: "#f07171", // bright red - bugs are problems
		Dark:  "#f26d78",
	}
	ColorTypeFeature = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
	ColorTypeTask = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
	ColorTypeEpic = lipgloss.AdaptiveColor{
		Light: "#d2a6ff", // purple - larger scope work
		Dark:  "#d2a6ff",
	}
	ColorTypeChore = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
	// Note: Gas Town-specific types (agent, role, rig) have been removed.
	// Use labels (gt:agent, gt:role, gt:rig) with custom styling if needed.

	// === Issue ID Color ===
	// IDs use standard text color - subtle, not attention-grabbing
	ColorID = lipgloss.AdaptiveColor{
		Light: "", // standard text color
		Dark:  "",
	}
)

// Core styles - consistent across all commands
var (
	PassStyle   = lipgloss.NewStyle().Foreground(ColorPass)
	WarnStyle   = lipgloss.NewStyle().Foreground(ColorWarn)
	FailStyle   = lipgloss.NewStyle().Foreground(ColorFail)
	MutedStyle  = lipgloss.NewStyle().Foreground(ColorMuted)
	AccentStyle = lipgloss.NewStyle().Foreground(ColorAccent)
)

// Issue ID style
var IDStyle = lipgloss.NewStyle().Foreground(ColorID)

// Status styles for workflow states
var (
	StatusOpenStyle       = lipgloss.NewStyle().Foreground(ColorStatusOpen)
	StatusInProgressStyle = lipgloss.NewStyle().Foreground(ColorStatusInProgress)
	StatusClosedStyle     = lipgloss.NewStyle().Foreground(ColorStatusClosed)
	StatusBlockedStyle    = lipgloss.NewStyle().Foreground(ColorStatusBlocked)
	StatusPinnedStyle     = lipgloss.NewStyle().Foreground(ColorStatusPinned)
	StatusHookedStyle     = lipgloss.NewStyle().Foreground(ColorStatusHooked)
)

// Priority styles
var (
	PriorityP0Style = lipgloss.NewStyle().Foreground(ColorPriorityP0).Bold(true)
	PriorityP1Style = lipgloss.NewStyle().Foreground(ColorPriorityP1)
	PriorityP2Style = lipgloss.NewStyle().Foreground(ColorPriorityP2)
	PriorityP3Style = lipgloss.NewStyle().Foreground(ColorPriorityP3)
	PriorityP4Style = lipgloss.NewStyle().Foreground(ColorPriorityP4)
)

// Type styles for issue categories
var (
	TypeBugStyle     = lipgloss.NewStyle().Foreground(ColorTypeBug)
	TypeFeatureStyle = lipgloss.NewStyle().Foreground(ColorTypeFeature)
	TypeTaskStyle    = lipgloss.NewStyle().Foreground(ColorTypeTask)
	TypeEpicStyle    = lipgloss.NewStyle().Foreground(ColorTypeEpic)
	TypeChoreStyle   = lipgloss.NewStyle().Foreground(ColorTypeChore)
	// Note: Gas Town-specific type styles (agent, role, rig) have been removed.
)

// CategoryStyle for section headers - bold with accent color
var CategoryStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

// Status icons - consistent semantic indicators
const (
	IconPass = "✓"
	IconWarn = "⚠"
	IconFail = "✖"
	IconSkip = "-"
	IconInfo = "ℹ"
)

// Issue status icons - used consistently across all commands
// Design principle: icons > text labels for scannability
// IMPORTANT: Use small Unicode symbols, NOT emoji-style icons (🔴🟠 etc.)
// Emoji blobs cause cognitive overload and break visual consistency
const (
	StatusIconOpen       = "○" // available to work (hollow circle)
	StatusIconInProgress = "◐" // active work (half-filled)
	StatusIconBlocked    = "●" // needs attention (filled circle)
	StatusIconClosed     = "✓" // completed (checkmark)
	StatusIconDeferred   = "❄" // scheduled for later (snowflake)
	StatusIconPinned     = "📌" // elevated priority
)

// Priority icon - small filled circle, colored by priority level
// IMPORTANT: Use this small circle, NOT emoji blobs (🔴🟠🟡🔵⚪)
const PriorityIcon = "●"

// RenderStatusIcon returns the appropriate icon for a status with semantic coloring
// This is the canonical source for status icon rendering - use this everywhere
func RenderStatusIcon(status string) string {
	switch status {
	case "open":
		return StatusIconOpen // no color - available but not urgent
	case "in_progress":
		return StatusInProgressStyle.Render(StatusIconInProgress)
	case "blocked":
		return StatusBlockedStyle.Render(StatusIconBlocked)
	case "closed":
		return StatusClosedStyle.Render(StatusIconClosed)
	case "deferred":
		return MutedStyle.Render(StatusIconDeferred)
	case "pinned":
		return StatusPinnedStyle.Render(StatusIconPinned)
	default:
		return "?" // unknown status
	}
}

// GetStatusIcon returns just the icon character without styling
// Useful when you need to apply custom styling or for non-TTY output
func GetStatusIcon(status string) string {
	switch status {
	case "open":
		return StatusIconOpen
	case "in_progress":
		return StatusIconInProgress
	case "blocked":
		return StatusIconBlocked
	case "closed":
		return StatusIconClosed
	case "deferred":
		return StatusIconDeferred
	case "pinned":
		return StatusIconPinned
	default:
		return "?"
	}
}

// GetStatusStyle returns the lipgloss style for a given status
// Use this when you need to apply the semantic color to custom text
// Example: ui.GetStatusStyle("in_progress").Render(myCustomText)
func GetStatusStyle(status string) lipgloss.Style {
	switch status {
	case "in_progress":
		return StatusInProgressStyle
	case "blocked":
		return StatusBlockedStyle
	case "closed":
		return StatusClosedStyle
	case "deferred":
		return MutedStyle
	case "pinned":
		return StatusPinnedStyle
	case "hooked":
		return StatusHookedStyle
	default: // open and others - no special styling
		return lipgloss.NewStyle()
	}
}

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

// === Issue Component Renderers ===

// RenderID renders an issue ID with semantic styling
func RenderID(id string) string {
	return IDStyle.Render(id)
}

// RenderStatus renders a status with semantic styling
// in_progress/blocked/pinned get color; open/closed use standard text
func RenderStatus(status string) string {
	switch status {
	case "in_progress":
		return StatusInProgressStyle.Render(status)
	case "blocked":
		return StatusBlockedStyle.Render(status)
	case "pinned":
		return StatusPinnedStyle.Render(status)
	case "hooked":
		return StatusHookedStyle.Render(status)
	case "closed":
		return StatusClosedStyle.Render(status)
	default: // open and others
		return StatusOpenStyle.Render(status)
	}
}

// RenderPriority renders a priority level with semantic styling
// Format: ● P0 (icon + label)
// P0/P1 get color; P2/P3/P4 use standard text
func RenderPriority(priority int) string {
	label := fmt.Sprintf("%s P%d", PriorityIcon, priority)
	switch priority {
	case 0:
		return PriorityP0Style.Render(label)
	case 1:
		return PriorityP1Style.Render(label)
	case 2:
		return PriorityP2Style.Render(label)
	case 3:
		return PriorityP3Style.Render(label)
	case 4:
		return PriorityP4Style.Render(label)
	default:
		return label
	}
}

// RenderPriorityCompact renders just the priority label without icon
// Use when space is constrained or icon would be redundant
func RenderPriorityCompact(priority int) string {
	label := fmt.Sprintf("P%d", priority)
	switch priority {
	case 0:
		return PriorityP0Style.Render(label)
	case 1:
		return PriorityP1Style.Render(label)
	case 2:
		return PriorityP2Style.Render(label)
	case 3:
		return PriorityP3Style.Render(label)
	case 4:
		return PriorityP4Style.Render(label)
	default:
		return label
	}
}

// RenderType renders an issue type with semantic styling
// bugs and epics get color; all other types use standard text
// Note: Gas Town-specific types (agent, role, rig) now fall through to default
func RenderType(issueType string) string {
	switch issueType {
	case "bug":
		return TypeBugStyle.Render(issueType)
	case "feature":
		return TypeFeatureStyle.Render(issueType)
	case "task":
		return TypeTaskStyle.Render(issueType)
	case "epic":
		return TypeEpicStyle.Render(issueType)
	case "chore":
		return TypeChoreStyle.Render(issueType)
	default:
		return issueType
	}
}

// RenderIssueCompact renders a compact one-line issue summary
// Format: ID [Priority] [Type] Status - Title
// When status is "closed", the entire line is dimmed to show it's done
func RenderIssueCompact(id string, priority int, issueType, status, title string) string {
	line := fmt.Sprintf("%s [P%d] [%s] %s - %s",
		id, priority, issueType, status, title)
	if status == "closed" {
		// Entire line is dimmed - visually shows "done"
		return StatusClosedStyle.Render(line)
	}
	return fmt.Sprintf("%s [%s] [%s] %s - %s",
		RenderID(id),
		RenderPriority(priority),
		RenderType(issueType),
		RenderStatus(status),
		title,
	)
}

// RenderPriorityForStatus renders priority with color only if not closed
func RenderPriorityForStatus(priority int, status string) string {
	if status == "closed" {
		return fmt.Sprintf("P%d", priority)
	}
	return RenderPriority(priority)
}

// RenderTypeForStatus renders type with color only if not closed
func RenderTypeForStatus(issueType, status string) string {
	if status == "closed" {
		return issueType
	}
	return RenderType(issueType)
}

// RenderClosedLine renders an entire line in the closed/dimmed style
func RenderClosedLine(line string) string {
	return StatusClosedStyle.Render(line)
}

// BoldStyle for emphasis
var BoldStyle = lipgloss.NewStyle().Bold(true)

// CommandStyle for command names - subtle contrast, not attention-grabbing
var CommandStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
	Light: "#5c6166", // slightly darker than standard
	Dark:  "#bfbdb6", // slightly brighter than standard
})

// RenderBold renders text in bold
func RenderBold(s string) string {
	return BoldStyle.Render(s)
}

// RenderCommand renders a command name with subtle styling
func RenderCommand(s string) string {
	return CommandStyle.Render(s)
}
