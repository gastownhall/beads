package main

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// obsidianCheckbox maps bd status to Obsidian Tasks checkbox syntax
var obsidianCheckbox = map[types.Status]string{
	types.StatusOpen:       "- [ ]",
	types.StatusInProgress: "- [/]",
	types.StatusBlocked:    "- [c]",
	types.StatusClosed:     "- [x]",
	types.StatusDeferred:   "- [-]",
	types.StatusPinned:     "- [n]", // Review/attention
	types.StatusHooked:     "- [/]", // Treat as in-progress
}

// obsidianPriority maps bd priority (0-4) to Obsidian priority emoji
var obsidianPriority = []string{
	"🔺", // 0 = critical/highest
	"⏫", // 1 = high
	"🔼", // 2 = medium
	"🔽", // 3 = low
	"⏬", // 4 = backlog/lowest
}

// obsidianTypeTag maps bd issue type to Obsidian tag (core types only)
// Gas Town types are custom types and will use their issue_type value as a tag.
var obsidianTypeTag = map[types.IssueType]string{
	types.TypeBug:     "#Bug",
	types.TypeFeature: "#Feature",
	types.TypeTask:    "#Task",
	types.TypeEpic:    "#Epic",
	types.TypeChore:   "#Chore",
}

// formatObsidianTask converts a single issue to Obsidian Tasks format
func formatObsidianTask(issue *types.Issue) string {
	var parts []string

	// Checkbox based on status
	checkbox, ok := obsidianCheckbox[issue.Status]
	if !ok {
		checkbox = "- [ ]" // default to open
	}
	parts = append(parts, checkbox)

	// Title first
	parts = append(parts, issue.Title)

	// Task ID with 🆔 emoji (official Obsidian Tasks format)
	parts = append(parts, fmt.Sprintf("🆔 %s", issue.ID))

	// Priority emoji
	if issue.Priority >= 0 && issue.Priority < len(obsidianPriority) {
		parts = append(parts, obsidianPriority[issue.Priority])
	}

	// Type tag
	if tag, ok := obsidianTypeTag[issue.IssueType]; ok {
		parts = append(parts, tag)
	}

	// Labels as tags
	for _, label := range issue.Labels {
		// Sanitize label for tag use (replace spaces with dashes)
		tag := "#" + strings.ReplaceAll(label, " ", "-")
		parts = append(parts, tag)
	}

	// Start date (created_at)
	parts = append(parts, fmt.Sprintf("🛫 %s", issue.CreatedAt.Format("2006-01-02")))

	// End date (closed_at) if closed
	if issue.ClosedAt != nil {
		parts = append(parts, fmt.Sprintf("✅ %s", issue.ClosedAt.Format("2006-01-02")))
	}

	// Dependencies with ⛔ emoji (official Obsidian Tasks "blocked by" format)
	// Include both blocks and parent-child relationships
	for _, dep := range issue.Dependencies {
		if dep.Type == types.DepBlocks || dep.Type == types.DepParentChild {
			parts = append(parts, fmt.Sprintf("⛔ %s", dep.DependsOnID))
		}
	}

	return strings.Join(parts, " ")
}

// groupIssuesByDate groups issues by their most recent activity date
func groupIssuesByDate(issues []*types.Issue) map[string][]*types.Issue {
	grouped := make(map[string][]*types.Issue)
	for _, issue := range issues {
		// Use the most recent date: closed_at > updated_at > created_at
		var date time.Time
		if issue.ClosedAt != nil {
			date = *issue.ClosedAt
		} else {
			date = issue.UpdatedAt
		}
		key := date.Format("2006-01-02")
		grouped[key] = append(grouped[key], issue)
	}
	return grouped
}

// buildParentChildMap builds a map of parent ID -> child issues from parent-child dependencies
func buildParentChildMap(issues []*types.Issue) (map[string][]*types.Issue, map[string]bool) {
	parentToChildren := make(map[string][]*types.Issue)
	isChild := make(map[string]bool)

	// Build lookup map
	issueByID := make(map[string]*types.Issue)
	for _, issue := range issues {
		issueByID[issue.ID] = issue
	}

	// Find parent-child relationships
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild {
				parentID := dep.DependsOnID
				parentToChildren[parentID] = append(parentToChildren[parentID], issue)
				isChild[issue.ID] = true
			}
		}
	}

	return parentToChildren, isChild
}

// writeObsidianExport writes issues in Obsidian Tasks markdown format
func writeObsidianExport(w io.Writer, issues []*types.Issue) error {
	// Write header
	if _, err := fmt.Fprintln(w, "# Changes Log"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	// Build parent-child hierarchy
	parentToChildren, isChild := buildParentChildMap(issues)

	// Group by date
	grouped := groupIssuesByDate(issues)

	// Get sorted dates (most recent first)
	dates := make([]string, 0, len(grouped))
	for date := range grouped {
		dates = append(dates, date)
	}
	// Sort descending (reverse order)
	slices.SortFunc(dates, func(a, b string) int {
		return cmp.Compare(b, a) // reverse: b before a for descending
	})

	// Write each date section
	for _, date := range dates {
		if _, err := fmt.Fprintf(w, "## %s\n\n", date); err != nil {
			return err
		}
		for _, issue := range grouped[date] {
			// Skip children - they'll be written under their parent
			if isChild[issue.ID] {
				continue
			}

			// Write parent issue
			line := formatObsidianTask(issue)
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}

			// Write children indented
			if children, ok := parentToChildren[issue.ID]; ok {
				for _, child := range children {
					childLine := "  " + formatObsidianTask(child)
					if _, err := fmt.Fprintln(w, childLine); err != nil {
						return err
					}
				}
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	return nil
}
