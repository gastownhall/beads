package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
)

// displayShowIssue displays a single issue (reusable for watch mode).
// Matches the full bd show output: header, metadata, content, labels, deps, comments.
// commentsTail is the --comments-tail render cap; see printComments.
func displayShowIssue(ctx context.Context, issueID string, commentsTail int) {
	displayShowIssueReturn(ctx, issueID, commentsTail)
}

// watchCommentFormatTime is the watch path's comment timestamp formatter.
// It reproduces, byte for byte, what the inline COMMENTS block here always
// did before printComments existed: UTC, unconditionally — this path does
// not read --local-time today (a pre-existing gap from the non-watch
// render), and this change does not alter that.
func watchCommentFormatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04")
}

// renderWatchComments prints the watch path's COMMENTS section, applying the
// same --comments-tail cap as the other two show routes, always through
// watchCommentFormatTime. Split out from displayShowIssueReturn as its own
// function so tests can exercise the watch path's exact render call —
// commentsTail plumbed all the way to printComments — without going through
// displayShowIssueReturn itself, which resolves the issue via a live store
// and so needs a real database.
func renderWatchComments(comments []*types.Comment, commentsTail int, issueID string) {
	printComments(comments, commentsTail, watchCommentFormatTime, issueID)
}

// singleIssueSnapshot builds a comparable string from a single issue's state
// so we can detect when the issue has changed between poll cycles.
func singleIssueSnapshot(issue *types.Issue) string {
	return fmt.Sprintf("%s:%s:%d", issue.ID, issue.Status, issue.UpdatedAt.UnixNano())
}

// watchIssue polls for changes to an issue and auto-refreshes the display (GH#654).
// Uses polling instead of fsnotify because Dolt stores data in a server-side
// database, not files — file watchers never fire. commentsTail is the
// --comments-tail render cap, threaded through to every render this loop
// does (initial and on each refresh); see printComments.
func watchIssue(ctx context.Context, issueID string, commentsTail int) {
	// Initial display and snapshot
	issue := displayShowIssueReturn(ctx, issueID, commentsTail)
	if issue == nil {
		return
	}
	lastSnapshot := singleIssueSnapshot(issue)

	fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")

	// Handle Ctrl+C — deferred Stop prevents signal handler leak
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	pollInterval := 2 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nStopped watching.\n")
			return
		case <-ticker.C:
			issue := fetchIssue(ctx, issueID)
			if issue == nil {
				continue
			}
			snap := singleIssueSnapshot(issue)
			if snap != lastSnapshot {
				lastSnapshot = snap
				displayShowIssue(ctx, issueID, commentsTail)
				fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
			}
		}
	}
}

// fetchIssue retrieves a single issue by ID, returning nil on error.
func fetchIssue(ctx context.Context, issueID string) *types.Issue {
	result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
	if result != nil {
		defer result.Close()
	}
	if err != nil || result == nil || result.Issue == nil {
		return nil
	}
	return result.Issue
}

// displayShowIssueReturn displays a single issue and returns it for snapshot use.
func displayShowIssueReturn(ctx context.Context, issueID string, commentsTail int) *types.Issue {
	result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
	if result != nil {
		defer result.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching issue: %v\n", err)
		return nil
	}
	if result == nil || result.Issue == nil {
		fmt.Printf("Issue not found: %s\n", issueID)
		return nil
	}
	issue := result.Issue
	issueStore := result.Store

	// Display the issue header and metadata
	fmt.Println(formatIssueHeader(issue))
	fmt.Println(formatIssueMetadata(issue))

	// Content sections (matches standard bd show order)
	if issue.Description != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), uimd.RenderMarkdown(issue.Description))
	}
	if issue.Design != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESIGN"), uimd.RenderMarkdown(issue.Design))
	}
	if issue.Notes != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), uimd.RenderMarkdown(issue.Notes))
	}
	if issue.AcceptanceCriteria != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("ACCEPTANCE CRITERIA"), uimd.RenderMarkdown(issue.AcceptanceCriteria))
	}

	// Labels
	labels, _ := issueStore.GetLabels(ctx, issue.ID)
	if len(labels) > 0 {
		fmt.Printf("\n%s %s\n", ui.RenderBold("LABELS:"), strings.Join(labels, ", "))
	}

	// Dependencies (what this issue depends on)
	relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)
	depsWithMeta, _ := issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
	for _, sec := range groupDepSections(depsWithMeta, true, relatedSeen) {
		printDepSection(sec)
	}

	// Dependents (what depends on this issue)
	dependentsWithMeta, _ := issueStore.GetDependentsWithMetadata(ctx, issue.ID)
	for _, sec := range groupDepSections(dependentsWithMeta, false, relatedSeen) {
		printDepSection(sec)
	}

	// Related (bidirectional, deduplicated)
	printRelatedSection(relatedSeen)

	// Comments. watchCommentFormatTime hardcodes UTC — this path does not
	// thread --local-time today (a pre-existing discrepancy from the
	// non-watch render, left alone here; only the --comments-tail cap is
	// wired through).
	comments, _ := issueStore.GetIssueComments(ctx, issue.ID)
	renderWatchComments(comments, commentsTail, issue.ID)

	fmt.Println()
	return issue
}
