// Package ui provides terminal styling for beads CLI output.
// Uses the Ayu color theme with adaptive light/dark mode support.
package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/ansi"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	if !ShouldUseColor() {
		return // all colors remain NoColor, all styles remain empty
	}
	// Default to dark mode. Detecting terminal background via OSC 11 is
	// fragile and can leak escape sequences in hook contexts (GH#1303).
	initColors(true)
	initStyles()
}

// DisableColors resets all styles to plain text output.
// Called from hook contexts to prevent ANSI escape sequence leaks.
func DisableColors() {
	ColorPass = ansi.NoColor
	ColorWarn = ansi.NoColor
	ColorFail = ansi.NoColor
	ColorMuted = ansi.NoColor
	ColorAccent = ansi.NoColor
	ColorStatusOpen = ansi.NoColor
	ColorStatusInProgress = ansi.NoColor
	ColorStatusClosed = ansi.NoColor
	ColorStatusBlocked = ansi.NoColor
	ColorStatusPinned = ansi.NoColor
	ColorStatusHooked = ansi.NoColor
	ColorPriorityP0 = ansi.NoColor
	ColorPriorityP1 = ansi.NoColor
	ColorPriorityP2 = ansi.NoColor
	ColorPriorityP3 = ansi.NoColor
	ColorPriorityP4 = ansi.NoColor
	ColorTypeBug = ansi.NoColor
	ColorTypeFeature = ansi.NoColor
	ColorTypeTask = ansi.NoColor
	ColorTypeEpic = ansi.NoColor
	ColorTypeChore = ansi.NoColor
	ColorID = ansi.NoColor

	PassStyle = ansi.NewStyle()
	WarnStyle = ansi.NewStyle()
	FailStyle = ansi.NewStyle()
	MutedStyle = ansi.NewStyle()
	AccentStyle = ansi.NewStyle()
	IDStyle = ansi.NewStyle()
	StatusOpenStyle = ansi.NewStyle()
	StatusInProgressStyle = ansi.NewStyle()
	StatusClosedStyle = ansi.NewStyle()
	StatusBlockedStyle = ansi.NewStyle()
	StatusPinnedStyle = ansi.NewStyle()
	StatusHookedStyle = ansi.NewStyle()
	PriorityP0Style = ansi.NewStyle()
	PriorityP1Style = ansi.NewStyle()
	PriorityP2Style = ansi.NewStyle()
	PriorityP3Style = ansi.NewStyle()
	PriorityP4Style = ansi.NewStyle()
	TypeBugStyle = ansi.NewStyle()
	TypeFeatureStyle = ansi.NewStyle()
	TypeTaskStyle = ansi.NewStyle()
	TypeEpicStyle = ansi.NewStyle()
	TypeChoreStyle = ansi.NewStyle()
	CategoryStyle = ansi.NewStyle()
	BoldStyle = ansi.NewStyle()
	CommandStyle = ansi.NewStyle()
}

// IsAgentMode returns true if the CLI is running in agent-optimized mode.
func IsAgentMode() bool {
	if os.Getenv("BD_AGENT_MODE") == "1" {
		return true
	}
	if os.Getenv("CLAUDE_CODE") != "" {
		return true
	}
	return false
}

// Ayu theme color palette
var (
	ColorPass   ansi.Color = ansi.NoColor
	ColorWarn   ansi.Color = ansi.NoColor
	ColorFail   ansi.Color = ansi.NoColor
	ColorMuted  ansi.Color = ansi.NoColor
	ColorAccent ansi.Color = ansi.NoColor

	ColorStatusOpen       ansi.Color = ansi.NoColor
	ColorStatusInProgress ansi.Color = ansi.NoColor
	ColorStatusClosed     ansi.Color = ansi.NoColor
	ColorStatusBlocked    ansi.Color = ansi.NoColor
	ColorStatusPinned     ansi.Color = ansi.NoColor
	ColorStatusHooked     ansi.Color = ansi.NoColor

	ColorPriorityP0 ansi.Color = ansi.NoColor
	ColorPriorityP1 ansi.Color = ansi.NoColor
	ColorPriorityP2 ansi.Color = ansi.NoColor
	ColorPriorityP3 ansi.Color = ansi.NoColor
	ColorPriorityP4 ansi.Color = ansi.NoColor

	ColorTypeBug     ansi.Color = ansi.NoColor
	ColorTypeFeature ansi.Color = ansi.NoColor
	ColorTypeTask    ansi.Color = ansi.NoColor
	ColorTypeEpic    ansi.Color = ansi.NoColor
	ColorTypeChore   ansi.Color = ansi.NoColor

	ColorID ansi.Color = ansi.NoColor
)

func initColors(isDark bool) {
	ld := func(light, dark ansi.Color) ansi.Color {
		return ansi.LightDark(isDark, light, dark)
	}

	ColorPass = ld("#86b300", "#c2d94c")
	ColorWarn = ld("#f2ae49", "#ffb454")
	ColorFail = ld("#f07171", "#f07178")
	ColorMuted = ld("#828c99", "#6c7680")
	ColorAccent = ld("#399ee6", "#59c2ff")

	ColorStatusOpen = ansi.NoColor
	ColorStatusInProgress = ld("#f2ae49", "#ffb454")
	ColorStatusClosed = ld("#9099a1", "#8090a0")
	ColorStatusBlocked = ld("#f07171", "#f26d78")
	ColorStatusPinned = ld("#d2a6ff", "#d2a6ff")
	ColorStatusHooked = ld("#59c2ff", "#59c2ff")

	ColorPriorityP0 = ld("#f07171", "#f07178")
	ColorPriorityP1 = ld("#ff8f40", "#ff8f40")
	ColorPriorityP2 = ld("#e6b450", "#e6b450")
	ColorPriorityP3 = ansi.NoColor
	ColorPriorityP4 = ansi.NoColor

	ColorTypeBug = ld("#f07171", "#f26d78")
	ColorTypeFeature = ansi.NoColor
	ColorTypeTask = ansi.NoColor
	ColorTypeEpic = ld("#d2a6ff", "#d2a6ff")
	ColorTypeChore = ansi.NoColor

	ColorID = ansi.NoColor

	CommandStyle = ansi.NewStyle().Foreground(ld("#5c6166", "#bfbdb6"))
}

// Core styles
var (
	PassStyle   = ansi.NewStyle()
	WarnStyle   = ansi.NewStyle()
	FailStyle   = ansi.NewStyle()
	MutedStyle  = ansi.NewStyle()
	AccentStyle = ansi.NewStyle()
)

var IDStyle = ansi.NewStyle()

// Status styles
var (
	StatusOpenStyle       = ansi.NewStyle()
	StatusInProgressStyle = ansi.NewStyle()
	StatusClosedStyle     = ansi.NewStyle()
	StatusBlockedStyle    = ansi.NewStyle()
	StatusPinnedStyle     = ansi.NewStyle()
	StatusHookedStyle     = ansi.NewStyle()
)

// Priority styles
var (
	PriorityP0Style = ansi.NewStyle()
	PriorityP1Style = ansi.NewStyle()
	PriorityP2Style = ansi.NewStyle()
	PriorityP3Style = ansi.NewStyle()
	PriorityP4Style = ansi.NewStyle()
)

// Type styles
var (
	TypeBugStyle     = ansi.NewStyle()
	TypeFeatureStyle = ansi.NewStyle()
	TypeTaskStyle    = ansi.NewStyle()
	TypeEpicStyle    = ansi.NewStyle()
	TypeChoreStyle   = ansi.NewStyle()
)

var CategoryStyle = ansi.NewStyle()
var BoldStyle = ansi.NewStyle()
var CommandStyle = ansi.NewStyle()

func initStyles() {
	PassStyle = ansi.NewStyle().Foreground(ColorPass)
	WarnStyle = ansi.NewStyle().Foreground(ColorWarn)
	FailStyle = ansi.NewStyle().Foreground(ColorFail)
	MutedStyle = ansi.NewStyle().Foreground(ColorMuted)
	AccentStyle = ansi.NewStyle().Foreground(ColorAccent)

	IDStyle = ansi.NewStyle().Foreground(ColorID)

	StatusOpenStyle = ansi.NewStyle().Foreground(ColorStatusOpen)
	StatusInProgressStyle = ansi.NewStyle().Foreground(ColorStatusInProgress)
	StatusClosedStyle = ansi.NewStyle().Foreground(ColorStatusClosed)
	StatusBlockedStyle = ansi.NewStyle().Foreground(ColorStatusBlocked)
	StatusPinnedStyle = ansi.NewStyle().Foreground(ColorStatusPinned)
	StatusHookedStyle = ansi.NewStyle().Foreground(ColorStatusHooked)

	PriorityP0Style = ansi.NewStyle().Foreground(ColorPriorityP0).Bold(true)
	PriorityP1Style = ansi.NewStyle().Foreground(ColorPriorityP1)
	PriorityP2Style = ansi.NewStyle().Foreground(ColorPriorityP2)
	PriorityP3Style = ansi.NewStyle().Foreground(ColorPriorityP3)
	PriorityP4Style = ansi.NewStyle().Foreground(ColorPriorityP4)

	TypeBugStyle = ansi.NewStyle().Foreground(ColorTypeBug)
	TypeFeatureStyle = ansi.NewStyle().Foreground(ColorTypeFeature)
	TypeTaskStyle = ansi.NewStyle().Foreground(ColorTypeTask)
	TypeEpicStyle = ansi.NewStyle().Foreground(ColorTypeEpic)
	TypeChoreStyle = ansi.NewStyle().Foreground(ColorTypeChore)

	CategoryStyle = ansi.NewStyle().Bold(true).Foreground(ColorAccent)
	BoldStyle = ansi.NewStyle().Bold(true)
}

// Status icons
const (
	IconPass = "✓"
	IconWarn = "⚠"
	IconFail = "✖"
	IconSkip = "-"
	IconInfo = "ℹ"
)

const (
	StatusIconOpen       = "○"
	StatusIconInProgress = "◐"
	StatusIconBlocked    = "●"
	StatusIconClosed     = "✓"
	StatusIconDeferred   = "❄"
	StatusIconPinned     = "📌"
	StatusIconCustom     = "◇"
)

const PriorityIcon = "●"

func RenderStatusIcon(status string) string {
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
	default:
		return StatusIconCustom
	}
}

func RenderStatusIconWithCategory(status string, category types.StatusCategory) string {
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

func GetStatusStyle(status string) ansi.Style {
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
	default:
		return ansi.NewStyle()
	}
}

const (
	TreeChild  = "⎿ "
	TreeLast   = "└─ "
	TreeIndent = "  "
)

const (
	SeparatorLight = "──────────────────────────────────────────"
	SeparatorHeavy = "══════════════════════════════════════════"
)

func RenderPass(s string) string     { return PassStyle.Render(s) }
func RenderWarn(s string) string     { return WarnStyle.Render(s) }
func RenderFail(s string) string     { return FailStyle.Render(s) }
func RenderMuted(s string) string    { return MutedStyle.Render(s) }
func RenderAccent(s string) string   { return AccentStyle.Render(s) }
func RenderCategory(s string) string { return CategoryStyle.Render(strings.ToUpper(s)) }
func RenderSeparator() string        { return MutedStyle.Render(SeparatorLight) }
func RenderPassIcon() string         { return PassStyle.Render(IconPass) }
func RenderWarnIcon() string         { return WarnStyle.Render(IconWarn) }
func RenderFailIcon() string         { return FailStyle.Render(IconFail) }
func RenderSkipIcon() string         { return MutedStyle.Render(IconSkip) }
func RenderInfoIcon() string         { return AccentStyle.Render(IconInfo) }
func RenderID(id string) string      { return IDStyle.Render(id) }

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
	default:
		return StatusOpenStyle.Render(status)
	}
}

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

func RenderIssueCompact(id string, priority int, issueType, status, title string) string {
	line := fmt.Sprintf("%s [P%d] [%s] %s - %s", id, priority, issueType, status, title)
	if status == "closed" {
		return StatusClosedStyle.Render(line)
	}
	return fmt.Sprintf("%s [%s] [%s] %s - %s",
		RenderID(id), RenderPriority(priority), RenderType(issueType), RenderStatus(status), title)
}

func RenderPriorityForStatus(priority int, status string) string {
	if status == "closed" {
		return fmt.Sprintf("P%d", priority)
	}
	return RenderPriority(priority)
}

func RenderTypeForStatus(issueType, status string) string {
	if status == "closed" {
		return issueType
	}
	return RenderType(issueType)
}

func RenderClosedLine(line string) string { return StatusClosedStyle.Render(line) }
func RenderBold(s string) string          { return BoldStyle.Render(s) }
func RenderCommand(s string) string       { return CommandStyle.Render(s) }
