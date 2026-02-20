package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
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
	// Use shared helpers from ui package
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
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
	// Priority uses ● icon with color, no brackets needed
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

// formatPrettyIssueWithContext formats an issue with optional parent epic annotation
func formatPrettyIssueWithContext(issue *types.Issue, parentEpic string) string {
	base := formatPrettyIssue(issue)
	if parentEpic == "" {
		return base
	}
	return base + " " + ui.RenderMuted("← "+parentEpic)
}

// formatIssueLong formats a single issue in long format to a buffer
func formatIssueLong(buf *strings.Builder, issue *types.Issue, labels []string) {
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
	if len(labels) > 0 {
		buf.WriteString(fmt.Sprintf("  Labels: %v\n", labels))
	}
	buf.WriteString("\n")
}

// formatAgentIssue formats a single issue in ultra-compact agent mode format
// Output: "ID: Title" with optional dependency info "(blocked by: X, blocks: Y)"
func formatAgentIssue(buf *strings.Builder, issue *types.Issue, blockedBy, blocks []string) {
	depInfo := formatDependencyInfo(blockedBy, blocks)
	if depInfo != "" {
		buf.WriteString(fmt.Sprintf("%s: %s %s\n", issue.ID, issue.Title, depInfo))
	} else {
		buf.WriteString(fmt.Sprintf("%s: %s\n", issue.ID, issue.Title))
	}
}

// formatDependencyInfo formats blocking dependency info for list output
// Returns "(blocked by: X, Y, blocks: Z)" or "" if no blocking dependencies
func formatDependencyInfo(blockedBy, blocks []string) string {
	if len(blockedBy) == 0 && len(blocks) == 0 {
		return ""
	}

	var parts []string
	if len(blockedBy) > 0 {
		parts = append(parts, fmt.Sprintf("blocked by: %s", strings.Join(blockedBy, ", ")))
	}
	if len(blocks) > 0 {
		parts = append(parts, fmt.Sprintf("blocks: %s", strings.Join(blocks, ", ")))
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
func getClosedBlockerIDs(ctx context.Context, s *dolt.DoltStore, allDeps map[string][]*types.Dependency) map[string]bool {
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
// Format: [icon] [pin] ID [Priority] [Type] @assignee [labels] - Title (blocked by: X, blocks: Y)
func formatIssueCompact(buf *strings.Builder, issue *types.Issue, labels []string, blockedBy, blocks []string) {
	labelsStr := ""
	if len(labels) > 0 {
		labelsStr = fmt.Sprintf(" %v", labels)
	}
	assigneeStr := ""
	if issue.Assignee != "" {
		assigneeStr = fmt.Sprintf(" @%s", issue.Assignee)
	}

	// Format dependency info
	depInfo := formatDependencyInfo(blockedBy, blocks)
	if depInfo != "" {
		depInfo = " " + depInfo
	}

	// Get styled status icon
	statusIcon := renderStatusIcon(issue.Status)

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
