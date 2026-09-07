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
func displayShowIssue(ctx context.Context, issueID string) {
	displayShowIssueReturn(ctx, issueID)
}

// singleIssueSnapshot builds a comparable string from a single issue's state
// so we can detect when the issue has changed between poll cycles.
func singleIssueSnapshot(issue *types.Issue) string {
	return fmt.Sprintf("%s:%s:%d", issue.ID, issue.Status, issue.UpdatedAt.UnixNano())
}

// watchIssue polls for changes to an issue and auto-refreshes the display (GH#654).
// Uses polling instead of fsnotify because Dolt stores data in a server-side
// database, not files — file watchers never fire.
func watchIssue(ctx context.Context, issueID string) {
	// Initial display and snapshot
	issue := displayShowIssueReturn(ctx, issueID)
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
				displayShowIssue(ctx, issueID)
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
	depsWithMeta, depsErr := issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
	for _, sec := range groupDepSections(depsWithMeta, true, relatedSeen) {
		printDepSection(sec)
	}

	// Dependents (what depends on this issue)
	dependentsWithMeta, dependentsErr := issueStore.GetDependentsWithMetadata(ctx, issue.ID)
	for _, sec := range groupDepSections(dependentsWithMeta, false, relatedSeen) {
		printDepSection(sec)
	}

	// Shared with the non-watch path in show.go so the two renders cannot
	// drift apart in what they disclose (be-lpi).
	warnUnresolvableDepEdges(ctx, issueStore, issue.ID,
		depListing{rows: len(depsWithMeta), err: depsErr},
		depListing{rows: len(dependentsWithMeta), err: dependentsErr})

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

// unresolvableDepCounter is the slice of the store warnUnresolvableDepEdges
// reads. Both counts are O(1) aggregate queries over the dependency tables.
// depListing is one direction's rendered edge list: how many rows reached the
// screen, and whether the read that produced them succeeded. The error is the
// load-bearing half — a failed read and a genuinely short one are both an
// empty slice, and only the second means an edge could not be represented.
type depListing struct {
	rows int
	err  error
}

type unresolvableDepCounter interface {
	CountDependencies(ctx context.Context, issueID string) (int64, error)
	CountDependents(ctx context.Context, issueID string) (int64, error)
}

// warnUnresolvableDepEdges prints a stderr-only notice when an issue has
// dependency edges that the rendered listings could not show.
//
// GetDependenciesWithMetadata and GetDependentsWithMetadata answer with the
// ISSUES on the far end of each edge and skip any whose id has no row in this
// database — a cross-repo id or an `external:` reference, both of which live
// in depends_on_external, the one target column carrying no foreign key into
// issues (issueops.IsExternalDepTarget). The count queries have no such join,
// so they keep those edges. The difference is exactly the set of edges that
// are real, stored, and unrenderable from here.
//
// Until be-lpi this difference was silent, and `bd dep add x liveop-y`
// reported success while the `bd show x` a caller ran straight after showed
// no dependency at all — indistinguishable from the edge never having been
// written. The JSON detail view publishes the same fact as
// unresolvable_dependencies / unresolvable_dependents.
//
// stderr only, so the rendered issue on stdout is byte-identical for the
// common fully-local case — the same choice warnDroppedDepEdges makes in
// dep.go for the same reason. Best effort: a count error is swallowed, since
// the issue has already been rendered successfully by the time this runs.
func warnUnresolvableDepEdges(ctx context.Context, store unresolvableDepCounter, issueID string, deps, dependents depListing) {
	if store == nil {
		return
	}
	reported := false
	report := func(kind string, count int64, countErr error, listing depListing) {
		// BOTH reads have to have succeeded. The count alone cannot tell a
		// SHORT listing from a FAILED one — each leaves an empty slice — and
		// warning on the second turns a transient backend error into a claim
		// about the data, which is the more expensive of the two mistakes.
		if countErr != nil || listing.err != nil {
			return
		}
		missing := count - int64(listing.rows)
		if missing <= 0 {
			return
		}
		reported = true
		fmt.Fprintf(os.Stderr, "warning: %s has %d %s edge(s) whose far end has no row in this database (cross-repo/external) and are not shown above\n",
			issueID, missing, kind)
	}
	depCount, depErr := store.CountDependencies(ctx, issueID)
	report("dependency", depCount, depErr, deps)
	rdepCount, rdepErr := store.CountDependents(ctx, issueID)
	report("dependent", rdepCount, rdepErr, dependents)
	if reported {
		// Named once, after both directions, so an issue short on each gets
		// one pointer rather than two.
		fmt.Fprintf(os.Stderr, "For raw edge records, run: bd dep list %s %s\n", issueID, issueID)
	}
}
