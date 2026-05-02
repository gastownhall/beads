// Package ui provides terminal styling for beads CLI output.
// Uses the Ayu color theme with adaptive light/dark mode support.
package ui

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	if !ShouldUseColor() {
		return // all colors remain NoColor, all styles remain empty
	}
	// Detect dark background for adaptive colors.
	// Only probed when color is enabled (prevents OSC 11 leaks in hook contexts).
	isDark := lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	initColors(isDark)
	initStyles()
}

// DisableColors resets all styles to plain text output.
// Called from hook contexts to prevent ANSI escape sequence leaks.
func DisableColors() {
	// Reset all color vars to NoColor
	ColorPass = lipgloss.NoColor{}
	ColorWarn = lipgloss.NoColor{}
	ColorFail = lipgloss.NoColor{}
	ColorMuted = lipgloss.NoColor{}
	ColorAccent = lipgloss.NoColor{}
	ColorStatusOpen = lipgloss.NoColor{}
	ColorStatusInProgress = lipgloss.NoColor{}
	ColorStatusClosed = lipgloss.NoColor{}
	ColorStatusBlocked = lipgloss.NoColor{}
	ColorStatusPinned = lipgloss.NoColor{}
	ColorStatusHooked = lipgloss.NoColor{}
	ColorPriorityP0 = lipgloss.NoColor{}
	ColorPriorityP1 = lipgloss.NoColor{}
	ColorPriorityP2 = lipgloss.NoColor{}
	ColorPriorityP3 = lipgloss.NoColor{}
	ColorPriorityP4 = lipgloss.NoColor{}
	ColorTypeBug = lipgloss.NoColor{}
	ColorTypeFeature = lipgloss.NoColor{}
	ColorTypeTask = lipgloss.NoColor{}
	ColorTypeEpic = lipgloss.NoColor{}
	ColorTypeChore = lipgloss.NoColor{}
	ColorID = lipgloss.NoColor{}

	// Reset all styles to empty (no ANSI output)
	PassStyle = lipgloss.NewStyle()
	WarnStyle = lipgloss.NewStyle()
	FailStyle = lipgloss.NewStyle()
	MutedStyle = lipgloss.NewStyle()
	AccentStyle = lipgloss.NewStyle()
	IDStyle = lipgloss.NewStyle()
	StatusOpenStyle = lipgloss.NewStyle()
	StatusInProgressStyle = lipgloss.NewStyle()
	StatusClosedStyle = lipgloss.NewStyle()
	StatusBlockedStyle = lipgloss.NewStyle()
	StatusPinnedStyle = lipgloss.NewStyle()
	StatusHookedStyle = lipgloss.NewStyle()
	PriorityP0Style = lipgloss.NewStyle()
	PriorityP1Style = lipgloss.NewStyle()
	PriorityP2Style = lipgloss.NewStyle()
	PriorityP3Style = lipgloss.NewStyle()
	PriorityP4Style = lipgloss.NewStyle()
	TypeBugStyle = lipgloss.NewStyle()
	TypeFeatureStyle = lipgloss.NewStyle()
	TypeTaskStyle = lipgloss.NewStyle()
	TypeEpicStyle = lipgloss.NewStyle()
	TypeChoreStyle = lipgloss.NewStyle()
	CategoryStyle = lipgloss.NewStyle()
	BoldStyle = lipgloss.NewStyle()
	CommandStyle = lipgloss.NewStyle()
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
	ColorPass   color.Color = lipgloss.NoColor{}
	ColorWarn   color.Color = lipgloss.NoColor{}
	ColorFail   color.Color = lipgloss.NoColor{}
	ColorMuted  color.Color = lipgloss.NoColor{}
	ColorAccent color.Color = lipgloss.NoColor{}

	// === Workflow Status Colors ===
	ColorStatusOpen       color.Color = lipgloss.NoColor{}
	ColorStatusInProgress color.Color = lipgloss.NoColor{}
	ColorStatusClosed     color.Color = lipgloss.NoColor{}
	ColorStatusBlocked    color.Color = lipgloss.NoColor{}
	ColorStatusPinned     color.Color = lipgloss.NoColor{}
	ColorStatusHooked     color.Color = lipgloss.NoColor{}

	// === Priority Colors ===
	ColorPriorityP0 color.Color = lipgloss.NoColor{}
	ColorPriorityP1 color.Color = lipgloss.NoColor{}
	ColorPriorityP2 color.Color = lipgloss.NoColor{}
	ColorPriorityP3 color.Color = lipgloss.NoColor{}
	ColorPriorityP4 color.Color = lipgloss.NoColor{}

	// === Issue Type Colors ===
	ColorTypeBug     color.Color = lipgloss.NoColor{}
	ColorTypeFeature color.Color = lipgloss.NoColor{}
	ColorTypeTask    color.Color = lipgloss.NoColor{}
	ColorTypeEpic    color.Color = lipgloss.NoColor{}
	ColorTypeChore   color.Color = lipgloss.NoColor{}
	// Note: Orchestrator-specific types (agent, role, rig) have been removed.
	// Use labels (gt:agent, gt:role, gt:rig) with custom styling if needed.

	// === Issue ID Color ===
	ColorID color.Color = lipgloss.NoColor{}
)

// initColors sets adaptive light/dark color values.
func initColors(isDark bool) {
	ld := lipgloss.LightDark(isDark)

	ColorPass = ld(lipgloss.Color("#86b300"), lipgloss.Color("#c2d94c"))
	ColorWarn = ld(lipgloss.Color("#f2ae49"), lipgloss.Color("#ffb454"))
	ColorFail = ld(lipgloss.Color("#f07171"), lipgloss.Color("#f07178"))
	ColorMuted = ld(lipgloss.Color("#828c99"), lipgloss.Color("#6c7680"))
	ColorAccent = ld(lipgloss.Color("#399ee6"), lipgloss.Color("#59c2ff"))

	// Workflow status colors — empty strings mean standard text color (NoColor)
	ColorStatusOpen = lipgloss.NoColor{} // standard text
	ColorStatusInProgress = ld(lipgloss.Color("#f2ae49"), lipgloss.Color("#ffb454"))
	ColorStatusClosed = ld(lipgloss.Color("#9099a1"), lipgloss.Color("#8090a0"))
	ColorStatusBlocked = ld(lipgloss.Color("#f07171"), lipgloss.Color("#f26d78"))
	ColorStatusPinned = ld(lipgloss.Color("#d2a6ff"), lipgloss.Color("#d2a6ff"))
	ColorStatusHooked = ld(lipgloss.Color("#59c2ff"), lipgloss.Color("#59c2ff"))

	// Priority colors — only P0/P1/P2 get color
	ColorPriorityP0 = ld(lipgloss.Color("#f07171"), lipgloss.Color("#f07178"))
	ColorPriorityP1 = ld(lipgloss.Color("#ff8f40"), lipgloss.Color("#ff8f40"))
	ColorPriorityP2 = ld(lipgloss.Color("#e6b450"), lipgloss.Color("#e6b450"))
	ColorPriorityP3 = lipgloss.NoColor{} // neutral
	ColorPriorityP4 = lipgloss.NoColor{} // neutral

	// Issue type colors — only bugs and epics get color
	ColorTypeBug = ld(lipgloss.Color("#f07171"), lipgloss.Color("#f26d78"))
	ColorTypeFeature = lipgloss.NoColor{} // standard text
	ColorTypeTask = lipgloss.NoColor{}    // standard text
	ColorTypeEpic = ld(lipgloss.Color("#d2a6ff"), lipgloss.Color("#d2a6ff"))
	ColorTypeChore = lipgloss.NoColor{} // standard text

	ColorID = lipgloss.NoColor{} // standard text

	// Command style - uses adaptive color for subtle contrast
	CommandStyle = lipgloss.NewStyle().Foreground(
		ld(lipgloss.Color("#bfbdb6"), lipgloss.Color("#5c6166")),
	)
}

// Core styles - consistent across all commands
var (
	PassStyle   = lipgloss.NewStyle()
	WarnStyle   = lipgloss.NewStyle()
	FailStyle   = lipgloss.NewStyle()
	MutedStyle  = lipgloss.NewStyle()
	AccentStyle = lipgloss.NewStyle()
)

// Issue ID style
var IDStyle = lipgloss.NewStyle()

// Status styles for workflow states
var (
	StatusOpenStyle       = lipgloss.NewStyle()
	StatusInProgressStyle = lipgloss.NewStyle()
	StatusClosedStyle     = lipgloss.NewStyle()
	StatusBlockedStyle    = lipgloss.NewStyle()
	StatusPinnedStyle     = lipgloss.NewStyle()
	StatusHookedStyle     = lipgloss.NewStyle()
)

// Priority styles
var (
	PriorityP0Style = lipgloss.NewStyle()
	PriorityP1Style = lipgloss.NewStyle()
	PriorityP2Style = lipgloss.NewStyle()
	PriorityP3Style = lipgloss.NewStyle()
	PriorityP4Style = lipgloss.NewStyle()
)

// Type styles for issue categories
var (
	TypeBugStyle     = lipgloss.NewStyle()
	TypeFeatureStyle = lipgloss.NewStyle()
	TypeTaskStyle    = lipgloss.NewStyle()
	TypeEpicStyle    = lipgloss.NewStyle()
	TypeChoreStyle   = lipgloss.NewStyle()
	// Note: Orchestrator-specific type styles (agent, role, rig) have been removed.
)

// CategoryStyle for section headers - bold with accent color
var CategoryStyle = lipgloss.NewStyle()

// BoldStyle for emphasis
var BoldStyle = lipgloss.NewStyle()

// CommandStyle for command names - subtle contrast, not attention-grabbing
var CommandStyle = lipgloss.NewStyle()

// initStyles sets up styled versions using the current color values.
func initStyles() {
	PassStyle = lipgloss.NewStyle().Foreground(ColorPass)
	WarnStyle = lipgloss.NewStyle().Foreground(ColorWarn)
	FailStyle = lipgloss.NewStyle().Foreground(ColorFail)
	MutedStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	AccentStyle = lipgloss.NewStyle().Foreground(ColorAccent)

	IDStyle = lipgloss.NewStyle().Foreground(ColorID)

	StatusOpenStyle = lipgloss.NewStyle().Foreground(ColorStatusOpen)
	StatusInProgressStyle = lipgloss.NewStyle().Foreground(ColorStatusInProgress)
	StatusClosedStyle = lipgloss.NewStyle().Foreground(ColorStatusClosed)
	StatusBlockedStyle = lipgloss.NewStyle().Foreground(ColorStatusBlocked)
	StatusPinnedStyle = lipgloss.NewStyle().Foreground(ColorStatusPinned)
	StatusHookedStyle = lipgloss.NewStyle().Foreground(ColorStatusHooked)

	PriorityP0Style = lipgloss.NewStyle().Foreground(ColorPriorityP0).Bold(true)
	PriorityP1Style = lipgloss.NewStyle().Foreground(ColorPriorityP1)
	PriorityP2Style = lipgloss.NewStyle().Foreground(ColorPriorityP2)
	PriorityP3Style = lipgloss.NewStyle().Foreground(ColorPriorityP3)
	PriorityP4Style = lipgloss.NewStyle().Foreground(ColorPriorityP4)

	TypeBugStyle = lipgloss.NewStyle().Foreground(ColorTypeBug)
	TypeFeatureStyle = lipgloss.NewStyle().Foreground(ColorTypeFeature)
	TypeTaskStyle = lipgloss.NewStyle().Foreground(ColorTypeTask)
	TypeEpicStyle = lipgloss.NewStyle().Foreground(ColorTypeEpic)
	TypeChoreStyle = lipgloss.NewStyle().Foreground(ColorTypeChore)

	CategoryStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	BoldStyle = lipgloss.NewStyle().Bold(true)
	// CommandStyle is set in initColors where LightDark is available
}

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
	StatusIconCustom     = "◇" // custom/uncategorized status (diamond)
)

// Priority icon - small filled circle, colored by priority level
// IMPORTANT: Use this small circle, NOT emoji blobs (🔴🟠🟡🔵⚪)
const PriorityIcon = "●"

// RenderStatusIcon returns the appropriate icon for a status with semantic coloring.
// This is the canonical source for status icon rendering - use this everywhere.
// For custom statuses, call RenderStatusIconWithCategory for category-aware rendering.
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
		return StatusIconCustom // custom/unknown status
	}
}

// RenderStatusIconWithCategory returns the icon for a status, using category
// to determine icon/color for custom statuses.
func RenderStatusIconWithCategory(status string, category types.StatusCategory) string {
	// Try built-in first
	switch status {
	case "open":
		return StatusIconOpen
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
	}
	// Custom status — inherit from category
	switch category {
	case types.CategoryActive:
		return StatusIconOpen
	case types.CategoryWIP:
		return StatusInProgressStyle.Render(StatusIconInProgress)
	case types.CategoryDone:
		return StatusClosedStyle.Render(StatusIconClosed)
	case types.CategoryFrozen:
		return MutedStyle.Render(StatusIconDeferred)
	default:
		return StatusIconCustom
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
		return StatusIconCustom
	}
}

// GetStatusIconWithCategory returns the icon character for a status using category fallback.
func GetStatusIconWithCategory(status string, category types.StatusCategory) string {
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
	}
	switch category {
	case types.CategoryActive:
		return StatusIconOpen
	case types.CategoryWIP:
		return StatusIconInProgress
	case types.CategoryDone:
		return StatusIconClosed
	case types.CategoryFrozen:
		return StatusIconDeferred
	default:
		return StatusIconCustom
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
// Note: Orchestrator-specific types (agent, role, rig) now fall through to default
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

// RenderBold renders text in bold
func RenderBold(s string) string {
	return BoldStyle.Render(s)
}

// RenderCommand renders a command name with subtle styling
func RenderCommand(s string) string {
	return CommandStyle.Render(s)
}
