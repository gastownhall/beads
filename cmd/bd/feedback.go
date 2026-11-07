package main

import (
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
)

// formatFeedbackID returns "id — title" or just "id" based on output.title-length config.
func formatFeedbackID(id, title string) string {
	title = applyTitleConfig(title)
	if title == "" {
		return id
	}
	return id + " — " + title
}

// printedFeedbackID is formatFeedbackID for the lines bd close prints above
// its --continue hint (the issue it closed, the molecule that completed and
// the issues it unblocked), which a user can paste with the hint: the ID is
// spelled by printedIssueID and the title by remedy.PrintedValue, as the
// lines around the other store-read ID hints are, so pasting them runs no
// part of the ID and neither can redraw the hint. An ID bd mints is a plain
// word and prints as formatFeedbackID prints it.
func printedFeedbackID(id, title string) string {
	title = applyTitleConfig(title)
	if title == "" {
		return printedIssueID(id)
	}
	return printedIssueID(id) + " — " + remedy.PrintedValue(title)
}

// formatFeedbackIDParen returns "id (title)" for multi-ID messages (dep commands).
func formatFeedbackIDParen(id, title string) string {
	title = applyTitleConfig(title)
	if title == "" {
		return id
	}
	return id + " (" + title + ")"
}

// applyTitleConfig applies the output.title-length config to a title string.
// Returns empty string when titles should be hidden (<= 0).
func applyTitleConfig(title string) string {
	if title == "" {
		return ""
	}
	maxLen := config.GetInt("output.title-length")
	switch {
	case maxLen <= 0:
		return "" // hide titles
	default:
		return truncateTitle(title, maxLen)
	}
}

// issueTitleOrEmpty returns the title of an issue, or empty string if issue is nil.
func issueTitleOrEmpty(issue *types.Issue) string {
	if issue == nil {
		return ""
	}
	return issue.Title
}

// lookupTitle returns the title for an issue ID, or empty string on failure.
// Best-effort lookup for feedback messages — never fails the command.
func lookupTitle(id string) string {
	if store == nil || IsExternalRef(id) {
		return ""
	}
	issue, err := store.GetIssue(rootCtx, id)
	if err != nil || issue == nil {
		return ""
	}
	return issue.Title
}
