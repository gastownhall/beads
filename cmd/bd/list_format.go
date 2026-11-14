package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// parseTimeFlag parses time strings using the layered time parsing architecture.
// Supports compact durations (+6h, -1d), natural language (tomorrow, next monday),
// and absolute formats (2006-01-02, RFC3339).
func parseTimeFlag(s string) (time.Time, error) {
	return timeparsing.ParseRelativeTime(s, time.Now())
}

// pinIndicator returns a pushpin emoji prefix for pinned issues
func pinIndicator(issue *types.Issue) string {
	if issue.Pinned {
		return "📌 "
	}
	return ""
}

// Priority tags for pretty output - simple text, semantic colors applied via ui package
// Design principle: only P0/P1 get color for attention, P2-P4 are neutral
func renderPriorityTag(priority int) string {
	return ui.RenderPriority(priority)
}

// renderStatusIcon returns the status icon with semantic coloring applied
// Delegates to the shared ui.RenderStatusIcon for consistency across commands
func renderStatusIcon(status types.Status) string {
	return ui.RenderStatusIcon(string(status))
}

// formatPrettyIssue formats a single issue for pretty output
// Uses semantic colors: status icon colored, priority P0/P1 colored, rest neutral
func formatPrettyIssue(issue *types.Issue) string {
	return formatPrettyIssueGated(issue, nil)
}

// formatPrettyIssueGated is formatPrettyIssue with the derived gate marker
// (wy-j2upyy): when an open gate blocks the issue, the glyph column carries
// ⊘ instead of the stored status's icon. The rest of the row is untouched,
// and the stored status is untouched — this is a decoration over the same
// predicate `bd ready` filters on, so the listing stops disagreeing with it.
//
// The marker OUTRANKS the status icon, including ❄: one column can hold one
// glyph, and of the two reasons the work cannot start, the gate is the one a
// reader cannot discover from the row's own fields. It never outranks 📌,
// because types.SubjectCanBeGated refuses a pinned subject outright — a pinned
// bead is out of `bd ready` on its own account, not the gate's.
//
// gatedBy is the gate ids the ONE predicate selected (gatesByIssueID); the
// SubjectCanBeGated re-check is the same clause, not a second rule, and is
// here so a caller that hands over a stale map cannot invent a decoration.
func formatPrettyIssueGated(issue *types.Issue, gatedBy []string) string {
	// Use shared helpers from ui package
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	if len(gatedBy) > 0 && types.SubjectCanBeGated(issue) {
		statusIcon = ui.StatusBlockedStyle.Render(ui.StatusIconGated)
	}
	priorityTag := renderPriorityTag(issue.Priority)

	// Type badge - only show for notable types
	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = ui.TypeEpicStyle.Render("[epic]") + " "
	case "bug":
		typeBadge = ui.TypeBugStyle.Render("[bug]") + " "
	}

	// Format: STATUS_ICON ID PRIORITY [Type] Title
	// Closed issues: entire line is muted
	if issue.Status == types.StatusClosed {
		return fmt.Sprintf("%s %s %s %s%s",
			statusIcon,
			ui.RenderMuted(issue.ID),
			ui.RenderMuted(fmt.Sprintf("P%d", issue.Priority)),
			ui.RenderMuted(string(issue.IssueType)),
			ui.RenderMuted(" "+issue.Title))
	}

	return fmt.Sprintf("%s %s %s %s%s", statusIcon, issue.ID, priorityTag, typeBadge, issue.Title)
}

// formatPrettyIssueWithContext formats an issue with optional parent epic annotation
func formatPrettyIssueWithContext(issue *types.Issue, parentEpic string) string {
	base := formatPrettyIssue(issue)
	if parentEpic == "" {
		return base
	}
	return base + " " + ui.RenderMuted("← "+parentEpic)
}

// formatIssueLong formats a single issue in long format to a buffer.
// When labelsSkipped is true (AD-02 --skip-labels), the Labels: line shows
// "(suppressed by --skip-labels)" instead of the (empty) hydration result.
func formatIssueLong(buf *strings.Builder, issue *types.Issue, labels []string, labelsSkipped bool) {
	status := string(issue.Status)
	if status == "closed" {
		line := fmt.Sprintf("%s%s [P%d] [%s] %s\n  %s",
			pinIndicator(issue), issue.ID, issue.Priority,
			issue.IssueType, status, issue.Title)
		buf.WriteString(ui.RenderClosedLine(line))
		buf.WriteString("\n")
	} else {
		buf.WriteString(fmt.Sprintf("%s%s [%s] [%s] %s\n",
			pinIndicator(issue),
			ui.RenderID(issue.ID),
			ui.RenderPriority(issue.Priority),
			ui.RenderType(string(issue.IssueType)),
			ui.RenderStatus(status)))
		buf.WriteString(fmt.Sprintf("  %s\n", issue.Title))
	}
	if issue.Assignee != "" {
		buf.WriteString(fmt.Sprintf("  Assignee: %s\n", issue.Assignee))
	}
	// This is the one text rendering in the tree that prints a field --brief
	// drops, so it is the one that has to say so. Keyed off the ROW, not off
	// the flag: IsLitePartial travels with the issue, so a row that arrived
	// projected reads the same here whichever door set it. Without this the
	// listing is indistinguishable from one whose issues have no description,
	// which is the ambiguity the flag is otherwise careful to avoid.
	if issue.IsLitePartial {
		buf.WriteString("  Description: (omitted by --brief)\n")
	} else if desc := strings.TrimSpace(issue.Description); desc != "" {
		buf.WriteString("  Description:\n")
		for _, line := range strings.Split(desc, "\n") {
			buf.WriteString(fmt.Sprintf("    %s\n", line))
		}
	}
	if labelsSkipped {
		buf.WriteString("  Labels: (suppressed by --skip-labels)\n")
	} else if len(labels) > 0 {
		buf.WriteString(fmt.Sprintf("  Labels: %v\n", labels))
	}
	if hasCustomMetadata(issue) {
		if n := countMetadataKeys(issue); n > 0 {
			buf.WriteString(fmt.Sprintf("  Metadata: %d keys\n", n))
		} else {
			buf.WriteString("  Metadata: set\n")
		}
	}
	buf.WriteString("\n")
}

// formatAgentIssue formats a single issue in ultra-compact agent mode format
// Output: "ID: Title" with optional dependency info
// "(parent: X, blocked by: Y, blocks: Z, gated by: G)"
//
// Agent mode has no glyph column, so the gate rides in the parenthetical the
// line already carries (wy-j2upyy). It is the seat that most needs it:
// ui.IsAgentMode() is true whenever CLAUDE_CODE is set, and an agent reading a
// plain row cannot tell startable work from work a human has to release.
func formatAgentIssue(buf *strings.Builder, issue *types.Issue, blockedBy, blocks []string, parent string, gatedBy []string) {
	depInfo := formatDependencyInfo(blockedBy, blocks, parent, gatedBy)
	if depInfo != "" {
		buf.WriteString(fmt.Sprintf("%s: %s %s\n", issue.ID, issue.Title, depInfo))
	} else {
		buf.WriteString(fmt.Sprintf("%s: %s\n", issue.ID, issue.Title))
	}
}

// formatDependencyInfo formats dependency info for list output.
// Parent-child deps are shown as "parent: X" (structural), separate from "blocked by" (blocking). (bd-hcxu)
// Returns "(parent: X, blocked by: Y, blocks: Z, gated by: G)" or "" if no dependencies.
//
// A gate is also a blocker, so its id appears under BOTH "blocked by" and
// "gated by". The repetition is the point: "blocked by" says what to wait on,
// "gated by" says which of those is a gate — work nobody can start by doing
// more work. The clause is present exactly when the ONE predicate selected a
// gate (types.GateIsHolding + types.SubjectCanBeGated).
func formatDependencyInfo(blockedBy, blocks []string, parent string, gatedBy []string) string {
	if len(blockedBy) == 0 && len(blocks) == 0 && parent == "" && len(gatedBy) == 0 {
		return ""
	}

	var parts []string
	if parent != "" {
		parts = append(parts, fmt.Sprintf("parent: %s", parent))
	}
	if len(blockedBy) > 0 {
		parts = append(parts, fmt.Sprintf("blocked by: %s", strings.Join(blockedBy, ", ")))
	}
	if len(blocks) > 0 {
		parts = append(parts, fmt.Sprintf("blocks: %s", strings.Join(blocks, ", ")))
	}
	if len(gatedBy) > 0 {
		parts = append(parts, fmt.Sprintf("gated by: %s", strings.Join(gatedBy, ", ")))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// buildBlockingMaps builds maps of blocking dependencies from dependency records.
// Returns three maps: blockedByMap[issueID] = []IDs that block this issue,
// blocksMap[issueID] = []IDs that this issue blocks (excluding parent-child),
// and childrenMap[issueID] = []IDs that are children of this issue.
// Only includes dependencies where AffectsReadyWork() is true (blocks, parent-child, etc.)
//
// closedIDs is an optional set of issue IDs known to be closed. When provided,
// closed blockers are excluded from blockedByMap so that "blocked by" annotations
// only show active (open) blockers. This prevents stale annotations like
// "(blocked by: X)" when X has already been closed.
func buildBlockingMaps(allDeps map[string][]*types.Dependency, closedIDs map[string]bool) (blockedByMap, blocksMap, childrenMap map[string][]string) {
	blockedByMap = make(map[string][]string)
	blocksMap = make(map[string][]string)
	childrenMap = make(map[string][]string)

	for issueID, deps := range allDeps {
		for _, dep := range deps {
			// Only include blocking dependencies
			if !dep.Type.AffectsReadyWork() {
				continue
			}
			// Skip closed blockers in "blocked by" annotations — the dependency
			// record is preserved, but a closed blocker no longer blocks work.
			isClosed := closedIDs != nil && closedIDs[dep.DependsOnID]
			if !isClosed {
				blockedByMap[issueID] = append(blockedByMap[issueID], dep.DependsOnID)
			}
			// Separate parent-child from blocking relationships
			if dep.Type == types.DepParentChild {
				childrenMap[dep.DependsOnID] = append(childrenMap[dep.DependsOnID], issueID)
			} else if !isClosed {
				blocksMap[dep.DependsOnID] = append(blocksMap[dep.DependsOnID], issueID)
			}
		}
	}
	return blockedByMap, blocksMap, childrenMap
}

// getClosedBlockerIDs collects all unique blocker IDs from dependency records
// and returns the subset that are closed or unreachable. This is used to filter
// stale "blocked by" annotations in bd list output.
func getClosedBlockerIDs(ctx context.Context, s storage.DoltStorage, allDeps map[string][]*types.Dependency) map[string]bool {
	// Collect unique blocker IDs
	blockerIDs := make(map[string]bool)
	for _, deps := range allDeps {
		for _, dep := range deps {
			if dep.Type.AffectsReadyWork() {
				blockerIDs[dep.DependsOnID] = true
			}
		}
	}

	closedIDs := make(map[string]bool)
	for id := range blockerIDs {
		issue, err := s.GetIssue(ctx, id)
		if err != nil || issue == nil {
			// Treat missing or unreachable blockers as resolved — a blocker
			// that cannot be fetched cannot block work, matching the behavior
			// of computeBlockedIDs which only considers active issues.
			closedIDs[id] = true
			continue
		}
		if issue.Status == types.StatusClosed {
			closedIDs[id] = true
		}
	}
	return closedIDs
}

// formatIssueCompact formats a single issue in compact format to a buffer
// Uses status icons for better scanability - consistent with bd graph
// Format: [icon] [pin] ID [Priority] [Type] @assignee [labels] - Title (parent: X, blocked by: Y, blocks: Z)
func formatIssueCompact(buf *strings.Builder, issue *types.Issue, labels []string, blockedBy, blocks []string, parent string, gatedBy []string) {
	labelsStr := ""
	if len(labels) > 0 {
		labelsStr = fmt.Sprintf(" %v", labels)
	}
	assigneeStr := ""
	if issue.Assignee != "" {
		assigneeStr = fmt.Sprintf(" @%s", issue.Assignee)
	}

	// Format dependency info
	depInfo := formatDependencyInfo(blockedBy, blocks, parent, gatedBy)
	if depInfo != "" {
		depInfo = " " + depInfo
	}

	// Get styled status icon — override to blocked when issue has open blockers (GH#2858)
	statusIcon := renderStatusIcon(issue.Status)
	if len(blockedBy) > 0 && issue.Status == types.StatusOpen {
		statusIcon = renderStatusIcon(types.StatusBlocked)
	}
	// A gate is a blocker a reader cannot see in the row, so it takes the
	// column back off ● (wy-j2upyy). Same predicate as the tree view's glyph.
	if len(gatedBy) > 0 && types.SubjectCanBeGated(issue) {
		statusIcon = ui.StatusBlockedStyle.Render(ui.StatusIconGated)
	}

	if issue.Status == types.StatusClosed {
		// Closed issues: entire line muted (fades visually)
		line := fmt.Sprintf("%s %s%s [P%d] [%s]%s%s - %s%s",
			statusIcon, pinIndicator(issue), issue.ID, issue.Priority,
			issue.IssueType, assigneeStr, labelsStr, issue.Title, depInfo)
		buf.WriteString(ui.RenderClosedLine(line))
		buf.WriteString("\n")
	} else {
		// Active issues: status icon + semantic colors for priority/type
		buf.WriteString(fmt.Sprintf("%s %s%s [%s] [%s]%s%s - %s%s\n",
			statusIcon,
			pinIndicator(issue),
			ui.RenderID(issue.ID),
			ui.RenderPriority(issue.Priority),
			ui.RenderType(string(issue.IssueType)),
			assigneeStr, labelsStr, issue.Title, depInfo))
	}
}

// hasCustomMetadata returns true if the issue has non-empty custom metadata.
func hasCustomMetadata(issue *types.Issue) bool {
	if len(issue.Metadata) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(string(issue.Metadata))
	return trimmed != "{}" && trimmed != "null"
}

// countMetadataKeys returns the number of top-level keys in the issue's metadata JSON.
func countMetadataKeys(issue *types.Issue) int {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(issue.Metadata, &data); err != nil {
		return 0
	}
	return len(data)
}
