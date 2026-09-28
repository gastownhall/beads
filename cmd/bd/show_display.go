package main

import (
	"context"
	"errors"
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
	// Counts first — see readDepCounts for why the order matters.
	depCountsSnapshot := readDepCounts(ctx, issueStore, issue.ID)
	depsWithMeta, depsErr := issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
	for _, sec := range groupDepSections(depsWithMeta, true, relatedSeen) {
		printDepSection(sec)
	}

	// Dependents (what depends on this issue)
	dependentsWithMeta, dependentsErr := issueStore.GetDependentsWithMetadata(ctx, issue.ID)
	for _, sec := range groupDepSections(dependentsWithMeta, false, relatedSeen) {
		printDepSection(sec)
	}

	// Shared with the non-watch path in show.go and with proxiedRenderIssue in
	// show_proxied_server.go, so all THREE text renders of `bd show` disclose
	// the same fact (be-lpi). --refs and --children are deliberately out of
	// scope: they answer an alternate query with no count beside it to
	// contradict.
	warnUnresolvableDepEdges(issue.ID, depCountsSnapshot,
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

// depCount is one direction's aggregate, carried with its own error for the
// same reason depListing is.
type depCount struct {
	n   int64
	err error
}

type depCounts struct {
	deps       depCount
	dependents depCount
}

var errNoDepCounter = errors.New("no dependency counter available")

type unresolvableDepCounter interface {
	CountDependencies(ctx context.Context, issueID string) (int64, error)
	CountDependents(ctx context.Context, issueID string) (int64, error)
}

// readDepCounts and warnUnresolvableDepEdges are split so the COUNTS ARE READ
// BEFORE THE ROW LISTINGS, which all three call sites do. The store issues a
// connection per call, so a concurrent write can land between the count and the
// listing; counting first puts that skew on the safe side, because an edge
// ADDED in the window leaves the count stale-LOW and the difference goes
// negative and is suppressed. Counting afterwards would announce a freshly
// added local edge as unresolvable. A concurrent DELETE still produces a
// spurious notice — the residual, and the reason this is an ordering mitigation
// rather than a fix (be-lpi; the real fix is a shared snapshot or a direct
// count of edges whose target has no row).
//
// SCOPE OF THAT ORDER, because the split is easy to read as more than it is:
// it makes the order VISIBLE at the call sites, and does not enforce it.
// Nothing in this package fails if a later edit moves a readDepCounts call
// below its listings — measured, not assumed: moving it in all three of
// show.go, show_display.go and show_proxied_server.go leaves the whole cmd/bd
// selector green, including the embedded production-path test and the proxied
// integration test, because the unit test drives
// warnUnresolvableDepEdges directly and no CLI test can land a write inside
// the window. The only automated pin is one tier down, on
// TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable, which covers
// BuildIssueDetails rather than these three renders. Treat the order here as a
// convention carried by this comment.
//
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
func readDepCounts(ctx context.Context, store unresolvableDepCounter, issueID string) depCounts {
	if store == nil {
		return depCounts{deps: depCount{err: errNoDepCounter}, dependents: depCount{err: errNoDepCounter}}
	}
	var c depCounts
	c.deps.n, c.deps.err = store.CountDependencies(ctx, issueID)
	c.dependents.n, c.dependents.err = store.CountDependents(ctx, issueID)
	return c
}

func warnUnresolvableDepEdges(issueID string, counts depCounts, deps, dependents depListing) {
	report := func(kind string, count int64, countErr error, listing depListing) bool {
		// BOTH reads have to have succeeded. The count alone cannot tell a
		// SHORT listing from a FAILED one — each leaves an empty slice — and
		// warning on the second turns a transient backend error into a claim
		// about the data, which is the more expensive of the two mistakes.
		if countErr != nil || listing.err != nil {
			return false
		}
		missing := count - int64(listing.rows)
		if missing <= 0 {
			return false
		}
		fmt.Fprintf(os.Stderr, "warning: %s has %d %s edge(s) whose far end has no row in this database (cross-repo/external) and are not shown above\n",
			issueID, missing, kind)
		return true
	}
	outbound := report("dependency", counts.deps.n, counts.deps.err, deps)
	inbound := report("dependent", counts.dependents.n, counts.dependents.err, dependents)

	// The recovery pointer is OUTBOUND-ONLY, because the command it names is.
	// `bd dep list <id> <id>` reaches raw edge records through the duplicate-id
	// form, which dep.go:1101 selects on `batchMode && direction == "down"`
	// (--direction defaults to "down"); the other branch is the Relations
	// query, which has the very far-end gap being reported here. dep.go:1144
	// records in the repo's own words that "up" has the same gap and no
	// inbound EdgeReader role exists to close it. Printing the pointer for an
	// issue short only on DEPENDENT edges would hand the reader a command that
	// cannot show them, so that case says what is actually true instead.
	if outbound {
		// Named once, after both directions, so an issue short on each gets
		// one pointer rather than two.
		fmt.Fprintf(os.Stderr, "For raw edge records, run: bd dep list %s %s\n", issueID, issueID)
	}
	if inbound {
		fmt.Fprintln(os.Stderr, "The unrenderable dependent edges have no raw CLI listing yet: bd dep list is outbound-only.")
	}
}
