package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// formatShortIssue returns a compact one-line representation of an issue
// Format: STATUS_ICON ID PRIORITY [Type] Title
func formatShortIssue(issue *types.Issue) string {
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	priorityTag := ui.RenderPriority(issue.Priority)

	// Type badge only for notable types
	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = ui.TypeEpicStyle.Render("[epic]") + " "
	case "bug":
		typeBadge = ui.TypeBugStyle.Render("[bug]") + " "
	}

	// Closed issues: entire line is muted
	if issue.Status == types.StatusClosed {
		return fmt.Sprintf("%s %s %s %s%s",
			statusIcon,
			ui.RenderMuted(issue.ID),
			ui.RenderMuted(fmt.Sprintf("● P%d", issue.Priority)),
			ui.RenderMuted(string(issue.IssueType)),
			ui.RenderMuted(" "+issue.Title))
	}

	return fmt.Sprintf("%s %s %s %s%s", statusIcon, issue.ID, priorityTag, typeBadge, issue.Title)
}

// formatIssueHeader returns the Tufte-aligned header line
// Format: ID · Title   [Priority · STATUS]
// All elements in bd show get semantic colors since focus is on one issue
func formatIssueHeader(issue *types.Issue) string {
	// Get status icon and style
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	statusStyle := ui.GetStatusStyle(string(issue.Status))
	statusStr := statusStyle.Render(strings.ToUpper(string(issue.Status)))

	// Priority with semantic color (includes ● icon)
	priorityTag := ui.RenderPriority(issue.Priority)

	// Type badge for notable types
	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = " " + ui.TypeEpicStyle.Render("[EPIC]")
	case "bug":
		typeBadge = " " + ui.TypeBugStyle.Render("[BUG]")
	}

	// Compaction indicator
	tierEmoji := ""
	switch issue.CompactionLevel {
	case 1:
		tierEmoji = " 🗜️"
	case 2:
		tierEmoji = " 📦"
	}

	// Build header: STATUS_ICON ID · Title   [Priority · STATUS]
	idStyled := ui.RenderAccent(issue.ID)
	return fmt.Sprintf("%s %s%s · %s%s   [%s · %s]",
		statusIcon, idStyled, typeBadge, issue.Title, tierEmoji, priorityTag, statusStr)
}

// formatIssueMetadata returns the metadata line(s) with grouped info
// Format: Owner: user · Type: task
//
//	Created: 2026-01-06 · Updated: 2026-01-08
func formatIssueMetadata(issue *types.Issue) string {
	var lines []string

	// Line 1: Owner/Assignee · Type
	metaParts := []string{}
	if issue.CreatedBy != "" {
		metaParts = append(metaParts, fmt.Sprintf("Owner: %s", issue.CreatedBy))
	}
	if issue.Assignee != "" {
		metaParts = append(metaParts, fmt.Sprintf("Assignee: %s", issue.Assignee))
	}

	// Type with semantic color
	typeStr := string(issue.IssueType)
	switch issue.IssueType {
	case "epic":
		typeStr = ui.TypeEpicStyle.Render("epic")
	case "bug":
		typeStr = ui.TypeBugStyle.Render("bug")
	}
	metaParts = append(metaParts, fmt.Sprintf("Type: %s", typeStr))

	if len(metaParts) > 0 {
		lines = append(lines, strings.Join(metaParts, " · "))
	}

	// Line 2: Created · Updated · Due/Defer
	timeParts := []string{}
	timeParts = append(timeParts, fmt.Sprintf("Created: %s", issue.CreatedAt.Format("2006-01-02")))
	timeParts = append(timeParts, fmt.Sprintf("Updated: %s", issue.UpdatedAt.Format("2006-01-02")))

	if issue.DueAt != nil {
		timeParts = append(timeParts, fmt.Sprintf("Due: %s", issue.DueAt.Format("2006-01-02")))
	}
	if issue.DeferUntil != nil {
		timeParts = append(timeParts, fmt.Sprintf("Deferred: %s", issue.DeferUntil.Format("2006-01-02")))
	}
	if len(timeParts) > 0 {
		lines = append(lines, strings.Join(timeParts, " · "))
	}

	// Line 3: Close reason (if closed)
	if issue.Status == types.StatusClosed && issue.CloseReason != "" {
		lines = append(lines, ui.RenderMuted(fmt.Sprintf("Close reason: %s", issue.CloseReason)))
	}

	// Line 4: External ref (if exists)
	if issue.ExternalRef != nil && *issue.ExternalRef != "" {
		lines = append(lines, fmt.Sprintf("External: %s", *issue.ExternalRef))
	}
	if issue.SpecID != "" {
		lines = append(lines, fmt.Sprintf("Spec: %s", issue.SpecID))
	}

	// Line 5: Wisp type (if ephemeral with classification)
	if issue.Ephemeral && issue.WispType != "" {
		lines = append(lines, fmt.Sprintf("Wisp type: %s", ui.RenderMuted(string(issue.WispType))))
	}

	return strings.Join(lines, "\n")
}

// formatDependencyLine formats a single dependency with semantic colors
// Closed items get entire row muted - the work is done, no need for attention
func formatDependencyLine(prefix string, dep *types.IssueWithDependencyMetadata) string {
	// Status icon (always rendered with semantic color)
	statusIcon := ui.GetStatusIcon(string(dep.Status))

	// Closed items: mute entire row since the work is complete
	if dep.Status == types.StatusClosed {
		return fmt.Sprintf("  %s %s %s: %s %s",
			prefix, statusIcon,
			ui.RenderMuted(dep.ID),
			ui.RenderMuted(dep.Title),
			ui.RenderMuted(fmt.Sprintf("● P%d", dep.Priority)))
	}

	// Active items: ID with status color, priority with semantic color
	style := ui.GetStatusStyle(string(dep.Status))
	idStr := style.Render(dep.ID)
	priorityTag := ui.RenderPriority(dep.Priority)

	// Type indicator for epics/bugs
	typeStr := ""
	if dep.IssueType == "epic" {
		typeStr = ui.TypeEpicStyle.Render("(EPIC)") + " "
	} else if dep.IssueType == "bug" {
		typeStr = ui.TypeBugStyle.Render("(BUG)") + " "
	}

	return fmt.Sprintf("  %s %s %s: %s%s %s", prefix, statusIcon, idStr, typeStr, dep.Title, priorityTag)
}

// formatSimpleDependencyLine formats a dependency without metadata (fallback)
// Closed items get entire row muted - the work is done, no need for attention
func formatSimpleDependencyLine(prefix string, dep *types.Issue) string {
	statusIcon := ui.GetStatusIcon(string(dep.Status))

	// Closed items: mute entire row since the work is complete
	if dep.Status == types.StatusClosed {
		return fmt.Sprintf("  %s %s %s: %s %s",
			prefix, statusIcon,
			ui.RenderMuted(dep.ID),
			ui.RenderMuted(dep.Title),
			ui.RenderMuted(fmt.Sprintf("● P%d", dep.Priority)))
	}

	// Active items: use semantic colors
	style := ui.GetStatusStyle(string(dep.Status))
	idStr := style.Render(dep.ID)
	priorityTag := ui.RenderPriority(dep.Priority)

	return fmt.Sprintf("  %s %s %s: %s %s", prefix, statusIcon, idStr, dep.Title, priorityTag)
}

// formatIssueCustomMetadata renders the issue's custom JSON metadata field
// for bd show output. Returns empty string if no metadata is set.
// Top-level keys are displayed sorted alphabetically, one per line.
// Scalar values are shown inline; objects/arrays are shown as compact JSON.
func formatIssueCustomMetadata(issue *types.Issue) string {
	if len(issue.Metadata) == 0 {
		return ""
	}
	// Treat empty object as "no metadata"
	trimmed := strings.TrimSpace(string(issue.Metadata))
	if trimmed == "{}" || trimmed == "null" {
		return ""
	}

	var data map[string]any
	if err := json.Unmarshal(issue.Metadata, &data); err != nil {
		// Not a JSON object — show raw value
		return fmt.Sprintf("%s\n  %s", ui.RenderBold("METADATA"), trimmed)
	}
	if len(data) == 0 {
		return ""
	}

	// Sort keys for stable output
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var lines []string
	for _, k := range keys {
		v := data[k]
		lines = append(lines, fmt.Sprintf("  %s: %s", k, formatMetadataValue(v)))
	}

	return fmt.Sprintf("%s\n%s", ui.RenderBold("METADATA"), strings.Join(lines, "\n"))
}

// formatIssueLongExtras returns additional detail sections for --long mode.
// Only sections with data are included. Fields already shown in default mode are skipped.
func formatIssueLongExtras(issue *types.Issue, formatTime func(time.Time) string) string {
	var sections []string

	// Extended timestamps and closure details
	var closeParts []string
	if issue.ClosedAt != nil {
		closeParts = append(closeParts, fmt.Sprintf("  Closed at: %s", formatTime(*issue.ClosedAt)))
	}
	if issue.ClosedBySession != "" {
		closeParts = append(closeParts, fmt.Sprintf("  Closed by session: %s", issue.ClosedBySession))
	}
	if issue.EstimatedMinutes != nil {
		closeParts = append(closeParts, fmt.Sprintf("  Estimated: %d minutes", *issue.EstimatedMinutes))
	}
	if issue.SourceSystem != "" {
		closeParts = append(closeParts, fmt.Sprintf("  Source system: %s", issue.SourceSystem))
	}
	if issue.Sender != "" {
		closeParts = append(closeParts, fmt.Sprintf("  Sender: %s", issue.Sender))
	}
	if issue.Ephemeral {
		closeParts = append(closeParts, "  Ephemeral: yes")
	}
	if issue.Pinned {
		closeParts = append(closeParts, "  Pinned: yes")
	}
	if issue.IsTemplate {
		closeParts = append(closeParts, "  Template: yes")
	}
	if issue.MolType != "" {
		closeParts = append(closeParts, fmt.Sprintf("  Mol type: %s", issue.MolType))
	}
	if issue.WorkType != "" {
		closeParts = append(closeParts, fmt.Sprintf("  Work type: %s", issue.WorkType))
	}
	if len(closeParts) > 0 {
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("EXTENDED DETAILS"), strings.Join(closeParts, "\n")))
	}

	// Compaction details (beyond what default mode shows)
	if issue.CompactionLevel > 0 {
		var compactParts []string
		compactParts = append(compactParts, fmt.Sprintf("  Level: %d", issue.CompactionLevel))
		if issue.CompactedAt != nil {
			compactParts = append(compactParts, fmt.Sprintf("  Compacted at: %s", formatTime(*issue.CompactedAt)))
		}
		if issue.CompactedAtCommit != nil {
			compactParts = append(compactParts, fmt.Sprintf("  Compacted at commit: %s", *issue.CompactedAtCommit))
		}
		if issue.OriginalSize > 0 {
			compactParts = append(compactParts, fmt.Sprintf("  Original size: %d bytes", issue.OriginalSize))
		}
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("COMPACTION"), strings.Join(compactParts, "\n")))
	}

	// Agent identity fields
	var agentParts []string
	if issue.HookBead != "" {
		agentParts = append(agentParts, fmt.Sprintf("  Hook bead: %s", issue.HookBead))
	}
	if issue.RoleBead != "" {
		agentParts = append(agentParts, fmt.Sprintf("  Role bead: %s", issue.RoleBead))
	}
	if issue.AgentState != "" {
		agentParts = append(agentParts, fmt.Sprintf("  State: %s", issue.AgentState))
	}
	if issue.LastActivity != nil {
		agentParts = append(agentParts, fmt.Sprintf("  Last activity: %s", formatTime(*issue.LastActivity)))
	}
	if issue.RoleType != "" {
		agentParts = append(agentParts, fmt.Sprintf("  Role type: %s", issue.RoleType))
	}
	if issue.Rig != "" {
		agentParts = append(agentParts, fmt.Sprintf("  Rig: %s", issue.Rig))
	}
	if len(agentParts) > 0 {
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("AGENT IDENTITY"), strings.Join(agentParts, "\n")))
	}

	// Gate fields
	var gateParts []string
	if issue.AwaitType != "" {
		gateParts = append(gateParts, fmt.Sprintf("  Await type: %s", issue.AwaitType))
	}
	if issue.AwaitID != "" {
		gateParts = append(gateParts, fmt.Sprintf("  Await ID: %s", issue.AwaitID))
	}
	if issue.Timeout > 0 {
		gateParts = append(gateParts, fmt.Sprintf("  Timeout: %s", issue.Timeout))
	}
	if len(issue.Waiters) > 0 {
		gateParts = append(gateParts, fmt.Sprintf("  Waiters: %s", strings.Join(issue.Waiters, ", ")))
	}
	if len(gateParts) > 0 {
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("GATE"), strings.Join(gateParts, "\n")))
	}

	// Slot fields
	if issue.Holder != "" {
		sections = append(sections, fmt.Sprintf("%s\n  Holder: %s",
			ui.RenderBold("SLOT"), issue.Holder))
	}

	// Source tracing
	var sourceParts []string
	if issue.SourceFormula != "" {
		sourceParts = append(sourceParts, fmt.Sprintf("  Formula: %s", issue.SourceFormula))
	}
	if issue.SourceLocation != "" {
		sourceParts = append(sourceParts, fmt.Sprintf("  Location: %s", issue.SourceLocation))
	}
	if len(sourceParts) > 0 {
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("SOURCE TRACING"), strings.Join(sourceParts, "\n")))
	}

	// HOP fields
	var hopParts []string
	if issue.Creator != nil && !issue.Creator.IsEmpty() {
		hopParts = append(hopParts, fmt.Sprintf("  Creator: %s", issue.Creator.String()))
	}
	if issue.QualityScore != nil {
		hopParts = append(hopParts, fmt.Sprintf("  Quality score: %.2f", *issue.QualityScore))
	}
	if issue.Crystallizes {
		hopParts = append(hopParts, "  Crystallizes: yes")
	}
	if len(issue.Validations) > 0 {
		var vals []string
		for _, v := range issue.Validations {
			vals = append(vals, fmt.Sprintf("%s (%s)", v.Validator.String(), v.Outcome))
		}
		hopParts = append(hopParts, fmt.Sprintf("  Validations: %s", strings.Join(vals, ", ")))
	}
	if len(hopParts) > 0 {
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("HOP"), strings.Join(hopParts, "\n")))
	}

	// Bonded-from references
	if len(issue.BondedFrom) > 0 {
		var refs []string
		for _, b := range issue.BondedFrom {
			refs = append(refs, fmt.Sprintf("  %s (%s)", b.SourceID, b.BondType))
		}
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("BONDED FROM"), strings.Join(refs, "\n")))
	}

	// Event fields
	var eventParts []string
	if issue.EventKind != "" {
		eventParts = append(eventParts, fmt.Sprintf("  Kind: %s", issue.EventKind))
	}
	if issue.Actor != "" {
		eventParts = append(eventParts, fmt.Sprintf("  Actor: %s", issue.Actor))
	}
	if issue.Target != "" {
		eventParts = append(eventParts, fmt.Sprintf("  Target: %s", issue.Target))
	}
	if issue.Payload != "" {
		eventParts = append(eventParts, fmt.Sprintf("  Payload: %s", issue.Payload))
	}
	if len(eventParts) > 0 {
		sections = append(sections, fmt.Sprintf("%s\n%s",
			ui.RenderBold("EVENT"), strings.Join(eventParts, "\n")))
	}

	if len(sections) == 0 {
		return ""
	}
	return "\n" + strings.Join(sections, "\n\n") + "\n"
}

// formatMetadataValue formats a single metadata value for display.
// Strings are shown unquoted, numbers/bools as-is, objects/arrays as compact JSON.
func formatMetadataValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		// JSON numbers unmarshal as float64; show integers without decimal
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case nil:
		return "null"
	default:
		// Arrays and nested objects — compact JSON
		b, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	}
}
