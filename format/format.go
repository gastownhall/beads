// Package format provides public formatting functions for beads issues.
// These functions are used by gt and other consumers to render issue
// output without depending on the bd CLI.
package format

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// PrettyIssue formats a single issue in the standard one-line list format.
// Output: STATUS_ICON ID PRIORITY [Type] Title
func PrettyIssue(issue *types.Issue) string {
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	priorityTag := ui.RenderPriority(issue.Priority)

	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = ui.TypeEpicStyle.Render("[epic]") + " "
	case "bug":
		typeBadge = ui.TypeBugStyle.Render("[bug]") + " "
	}

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

// CompactIssue formats an issue with optional assignee, labels, and dependency info.
// Output: STATUS_ICON ID [Priority] [Type] @assignee [labels] - Title (deps)
func CompactIssue(issue *types.Issue, labels []string, blockedBy, blocks []string, parent string) string {
	var buf strings.Builder

	labelsStr := ""
	if len(labels) > 0 {
		labelsStr = fmt.Sprintf(" %v", labels)
	}
	assigneeStr := ""
	if issue.Assignee != "" {
		assigneeStr = fmt.Sprintf(" @%s", issue.Assignee)
	}

	depInfo := DependencyInfo(blockedBy, blocks, parent)
	if depInfo != "" {
		depInfo = " " + depInfo
	}

	statusIcon := ui.RenderStatusIcon(string(issue.Status))

	if issue.Status == types.StatusClosed {
		pinPrefix := ""
		if issue.Pinned {
			pinPrefix = "📌 "
		}
		line := fmt.Sprintf("%s %s%s [P%d] [%s]%s%s - %s%s",
			statusIcon, pinPrefix, issue.ID, issue.Priority,
			issue.IssueType, assigneeStr, labelsStr, issue.Title, depInfo)
		buf.WriteString(ui.RenderClosedLine(line))
	} else {
		pinPrefix := ""
		if issue.Pinned {
			pinPrefix = "📌 "
		}
		buf.WriteString(fmt.Sprintf("%s %s%s [%s] [%s]%s%s - %s%s",
			statusIcon,
			pinPrefix,
			ui.RenderID(issue.ID),
			ui.RenderPriority(issue.Priority),
			ui.RenderType(string(issue.IssueType)),
			assigneeStr, labelsStr, issue.Title, depInfo))
	}

	return buf.String()
}

// LongIssue formats a single issue in full detail.
func LongIssue(issue *types.Issue, labels []string) string {
	var buf strings.Builder

	status := string(issue.Status)
	if status == "closed" {
		pinPrefix := ""
		if issue.Pinned {
			pinPrefix = "📌 "
		}
		line := fmt.Sprintf("%s%s [P%d] [%s] %s\n  %s",
			pinPrefix, issue.ID, issue.Priority,
			issue.IssueType, status, issue.Title)
		buf.WriteString(ui.RenderClosedLine(line))
		buf.WriteString("\n")
	} else {
		pinPrefix := ""
		if issue.Pinned {
			pinPrefix = "📌 "
		}
		buf.WriteString(fmt.Sprintf("%s%s [%s] [%s] %s\n",
			pinPrefix,
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
	if issue.Description != "" {
		buf.WriteString(fmt.Sprintf("\n%s\n", issue.Description))
	}

	return buf.String()
}

// DependencyInfo formats dependency info string.
// Returns "(parent: X, blocked by: Y, blocks: Z)" or "" if no dependencies.
func DependencyInfo(blockedBy, blocks []string, parent string) string {
	if len(blockedBy) == 0 && len(blocks) == 0 && parent == "" {
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
	return "(" + strings.Join(parts, ", ") + ")"
}

// StatusIcon returns the status icon character without styling.
func StatusIcon(status string) string {
	switch status {
	case "open":
		return "○"
	case "in_progress":
		return "◐"
	case "blocked":
		return "●"
	case "closed":
		return "✓"
	case "deferred":
		return "❄"
	case "hooked":
		return "◇"
	case "pinned":
		return "📌"
	default:
		return "○"
	}
}

// ListSummary formats the summary footer for a list of issues.
func ListSummary(total int, byCounts map[string]int) string {
	if total == 0 {
		return "No issues found."
	}

	var parts []string
	for status, count := range byCounts {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, status))
		}
	}

	summary := fmt.Sprintf("Total: %d issues", total)
	if len(parts) > 0 {
		summary += " (" + strings.Join(parts, ", ") + ")"
	}
	return summary
}
