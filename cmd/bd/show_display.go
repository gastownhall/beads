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

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
)

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
// database, not files — file watchers never fire. commentsTail is the
// --comments-tail render cap, threaded through to every render this loop
// does (initial and on each refresh); see printComments.
func watchIssue(ctx context.Context, issueID string, commentsTail int) error {
	return runIssueWatch(ctx, issueWatchSource{
		render: func(ctx context.Context) *types.Issue { return displayShowIssueReturn(ctx, issueID, commentsTail) },
		fetch:  func(ctx context.Context) (*types.Issue, error) { return fetchIssue(ctx, issueID) },
	})
}

// runIssueWatch drives watchIssueLoop, waiting showWatchPollInterval after
// each poll, until Ctrl+C or SIGTERM.
// It fails when the initial render found nothing to watch; the render has
// already said why, so the failure is silent.
func runIssueWatch(ctx context.Context, src issueWatchSource) error {
	// Deferred Stop prevents signal handler leak
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	poll := func() <-chan time.Time { return time.After(showWatchPollInterval) }
	if !watchIssueLoop(ctx, src, poll, sigChan) {
		return SilentExit()
	}
	return nil
}

// watchIssueLoop renders the issue once, then after every wait re-reads it and
// redraws only when singleIssueSnapshot changes. A poll that fails — the
// issue was deleted, the backend blipped, a unit of work would not open — keeps
// the last render on screen and is retried after the next wait without
// printing: the watch is a live view, and a line (or, under --json, an error
// object) on every poll for a condition the user can already see would bury
// it. It returns when stop fires, or immediately, reporting false, when the
// initial render found nothing to watch.
//
// poll is called each time the loop starts waiting, which is after the
// previous fetch (and any redraw) has returned, so every poll is followed by
// a full wait. A fixed-rate ticker instead started the next fetch the moment
// a slow one returned (GH#3415), and a stop that arrives during a fetch now
// always wins over the next poll.
func watchIssueLoop(ctx context.Context, src issueWatchSource, poll func() <-chan time.Time, stop <-chan os.Signal) bool {
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
		case <-poll():
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
// commentsTail is the --comments-tail render cap; see printComments.
func displayShowIssueReturn(ctx context.Context, issueID string, commentsTail int) *types.Issue {
	result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
	if result != nil {
		defer result.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching issue: %s\n", remedy.PrintedMessage(err))
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
	// The dependent count first — see readDependentCount for why the order
	// matters. The outgoing edges come from the combined read so their
	// unrenderable far ends print in their relation sections (GH#5850).
	dependentsCount := readDependentCount(ctx, issueStore, issue.ID)
	// Best effort, as it always was: a failed read renders the issue
	// undecorated rather than not at all — and, as on the JSON side, says
	// nothing about unresolvable edges on the strength of a query that
	// never ran.
	depsWithMeta, missingEdges, _ := issueStore.GetDependenciesWithUnresolvable(ctx, issue.ID)
	for _, sec := range groupDepSections(depsWithMeta, missingEdges, true, relatedSeen) {
		printDepSection(sec)
	}

	// Dependents (what depends on this issue)
	dependentsWithMeta, dependentsErr := issueStore.GetDependentsWithMetadata(ctx, issue.ID)
	for _, sec := range groupDepSections(dependentsWithMeta, nil, false, relatedSeen) {
		printDepSection(sec)
	}

	// Shared with the non-watch path in show.go and with proxiedRenderIssue in
	// show_proxied_server.go, so all THREE text renders of `bd show` disclose
	// the same fact (be-lpi): outbound edges render in their sections, and the
	// inbound gap — which has no combined read yet — warns on stderr.
	// --refs and --children are deliberately out of scope: they answer an
	// alternate query with no count beside it to contradict.
	warnUnresolvableDependentEdges(issue.ID, dependentsCount,
		depListing{rows: len(dependentsWithMeta), err: dependentsErr})

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

// unresolvableDepCounter is the slice of the store the INBOUND unresolvable
// notice reads: one O(1) aggregate query over the dependency tables. The
// outbound disclosure needs no counter — since GH#5850 the three show renders
// read the combined outgoing records and print each missing far end in its
// relation section, so there is nothing left to warn about in that direction.
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

var errNoDepCounter = errors.New("no dependency counter available")

type unresolvableDepCounter interface {
	CountDependents(ctx context.Context, issueID string) (int64, error)
}

// readDependentCount and warnUnresolvableDependentEdges are split so the
// DEPENDENT COUNT IS READ BEFORE THE DEPENDENT LISTING, which all three call
// sites do. The store issues a connection per call, so a concurrent write can
// land between the count and the listing; counting first puts that skew on
// the safe side, because an edge ADDED in the window leaves the count
// stale-LOW and the difference goes negative and is suppressed. Counting
// afterwards would announce a freshly added local edge as unresolvable. A
// concurrent DELETE still produces a spurious INBOUND notice — the residual,
// and the reason this is an ordering mitigation rather than a fix (the real
// fix is the combined inbound read GH#5850 built for the outbound direction;
// no inbound counterpart exists yet — dep.go:1144 records that "up" has the
// same gap and no inbound EdgeReader role exists to close it).
//
// SCOPE OF THAT ORDER, because the split is easy to read as more than it is:
// it makes the order VISIBLE at the call sites, and does not enforce it.
// Nothing in this package fails if a later edit moves a readDependentCount
// call below its listing. The only automated pins are one tier down, on
// TestBuildIssueDetails_UnresolvableDependents and
// TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable, which
// cover BuildIssueDetails rather than these three renders. Treat the order
// here as a convention carried by this comment.
//
// warnUnresolvableDependentEdges prints a stderr-only notice when an issue
// has DEPENDENT edges the rendered listing could not show. The outbound
// direction no longer warns at all: its unrenderable far ends are rendered
// in their relation sections from the combined read's records
// (GetDependenciesWithUnresolvable), and a second notice naming edges the
// reader can see would be redundant.
//
// GetDependentsWithMetadata answers with the ISSUES on the other end of each
// inbound edge and skips any whose id has no row in this database — a cross-repo id
// or an `external:` reference, both of which live in
// depends_on_external/depends_on_wisp_id, the target columns carrying no
// foreign key into issues (issueops.IsExternalDepTarget). The count query has
// no such join, so it keeps those edges. The difference is exactly the set of
// inbound edges that are real, stored, and unrenderable from here.
//
// stderr only, so the rendered issue on stdout is byte-identical for the
// common fully-local case — the same choice warnDroppedDepEdges makes in
// dep.go for the same reason. Best effort: a count error is swallowed, since
// the issue has already been rendered successfully by the time this runs.
func readDependentCount(ctx context.Context, store unresolvableDepCounter, issueID string) depCount {
	if store == nil {
		return depCount{err: errNoDepCounter}
	}
	var n int64
	var err error
	n, err = store.CountDependents(ctx, issueID)
	return depCount{n: n, err: err}
}

func warnUnresolvableDependentEdges(issueID string, count depCount, dependents depListing) {
	// BOTH reads have to have succeeded. The count alone cannot tell a
	// SHORT listing from a FAILED one — each leaves an empty slice — and
	// warning on the second turns a transient backend error into a claim
	// about the data, which is the more expensive of the two mistakes.
	if count.err != nil || dependents.err != nil {
		return
	}
	missing := count.n - int64(dependents.rows)
	if missing <= 0 {
		return
	}
	// issueID is the store's, named as printedIssueID spells it so it cannot
	// redraw the bd dep list pointer dep.go prints for the outbound gap.
	fmt.Fprintf(os.Stderr, "warning: %s has %d dependent edge(s) whose far end has no row in this database (cross-repo/external) and are not shown above\n",
		printedIssueID(issueID), missing)
	// The recovery pointer is absent because the command it would name is
	// outbound-only: `bd dep list <id> <id>` reaches raw edge records through
	// the duplicate-id form (dep.go:1101), which reads "down" edges only.
	// Printing it here would hand the reader a command that cannot show these
	// edges, so say what is actually true instead.
	fmt.Fprintln(os.Stderr, "The unrenderable dependent edges have no raw CLI listing yet: bd dep list is outbound-only.")
}
