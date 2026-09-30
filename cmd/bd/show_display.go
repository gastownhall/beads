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

// singleIssueSnapshot builds a comparable string from a single issue's state
// so we can detect when the issue has changed between poll cycles.
func singleIssueSnapshot(issue *types.Issue) string {
	return fmt.Sprintf("%s:%s:%d", issue.ID, issue.Status, issue.UpdatedAt.UnixNano())
}

// issueWatchSource is what a `bd show --watch` loop reads from. render draws
// the full view and returns the issue it drew, or nil when there was nothing to
// draw (it reports why itself); fetch re-reads the issue for the change check
// and must not print anything, since the loop owns what a failed poll means.
// The direct store and the proxied-server provider each supply one, so both
// routes share the snapshot, redraw and poll-failure rules below instead of
// copying them.
type issueWatchSource struct {
	render func(ctx context.Context) *types.Issue
	fetch  func(ctx context.Context) (*types.Issue, error)
}

// showWatchPollInterval is how often a watched issue is re-read.
const showWatchPollInterval = 2 * time.Second

// watchIssue polls for changes to an issue and auto-refreshes the display (GH#654).
// Uses polling instead of fsnotify because Dolt stores data in a server-side
// database, not files — file watchers never fire.
func watchIssue(ctx context.Context, issueID string) error {
	return runIssueWatch(ctx, issueWatchSource{
		render: func(ctx context.Context) *types.Issue { return displayShowIssueReturn(ctx, issueID) },
		fetch:  func(ctx context.Context) (*types.Issue, error) { return fetchIssue(ctx, issueID) },
	})
}

// runIssueWatch drives watchIssueLoop on a real ticker until Ctrl+C or SIGTERM.
// It fails when the initial render found nothing to watch; the render has
// already said why, so the failure is silent.
func runIssueWatch(ctx context.Context, src issueWatchSource) error {
	// Deferred Stop prevents signal handler leak
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	ticker := time.NewTicker(showWatchPollInterval)
	defer ticker.Stop()

	if !watchIssueLoop(ctx, src, ticker.C, sigChan) {
		return SilentExit()
	}
	return nil
}

// watchIssueLoop renders the issue once, then on every tick re-reads it and
// redraws only when singleIssueSnapshot changes. A poll that fails — the
// issue was deleted, the backend blipped, a unit of work would not open — keeps
// the last render on screen and is retried on the next tick without printing:
// the watch is a live view, and a line (or, under --json, an error object) on
// every tick for a condition the user can already see would bury it. It
// returns when stop fires, or immediately, reporting false, when the initial
// render found nothing to watch.
func watchIssueLoop(ctx context.Context, src issueWatchSource, tick <-chan time.Time, stop <-chan os.Signal) bool {
	issue := src.render(ctx)
	if issue == nil {
		return false
	}
	lastSnapshot := singleIssueSnapshot(issue)

	fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")

	for {
		select {
		case <-stop:
			fmt.Fprintf(os.Stderr, "\nStopped watching.\n")
			return true
		case <-tick:
			issue, err := src.fetch(ctx)
			if err != nil || issue == nil {
				continue
			}
			snap := singleIssueSnapshot(issue)
			if snap != lastSnapshot {
				lastSnapshot = snap
				src.render(ctx)
				fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
			}
		}
	}
}

// fetchIssue retrieves a single issue by ID without printing anything.
func fetchIssue(ctx context.Context, issueID string) (*types.Issue, error) {
	result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
	if result != nil {
		defer result.Close()
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.Issue == nil {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}
	return result.Issue, nil
}

// displayShowIssueReturn displays a single issue and returns it for snapshot use.
// Matches the full bd show output: header, metadata, content, labels, deps, comments.
func displayShowIssueReturn(ctx context.Context, issueID string) *types.Issue {
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

	// Comments
	comments, _ := issueStore.GetIssueComments(ctx, issue.ID)
	if len(comments) > 0 {
		fmt.Printf("\n%s\n", ui.RenderBold("COMMENTS"))
		for _, comment := range comments {
			fmt.Printf("  %s %s\n", ui.RenderMuted(comment.CreatedAt.UTC().Format("2006-01-02 15:04")), comment.Author)
			rendered := uimd.RenderMarkdown(comment.Text)
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}

	fmt.Println()
	return issue
}
