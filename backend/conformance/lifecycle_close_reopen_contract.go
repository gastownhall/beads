package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract for the two Lifecycle verbs that own
// the close/reopen transition — publicops.Lifecycle.Close and
// publicops.Lifecycle.Reopen — written against what issueops/issueops.go
// promises rather than against what any one implementation does. Create and
// Update live in issue_operations_contract.go; splitting the role across two
// files keeps two slices off one file, the way the staging contract already
// splits the persistence-level assertions out.
//
// Every case here runs on all three implementations. That matters more than
// usual for these two verbs: the server-backed and embedded stores share one
// validate/execute body (internal/storage/issueops/execution.go ExecuteClose
// and ExecuteReopen), so they are ONE vote on the semantics, while the
// unit-of-work backend reaches the same row bodies through domain/db and then
// derives Changed by comparing post-state instead of reading the row-write
// facts (internal/storage/uow/issue_operations.go:419, 458). Every Changed,
// OpenChildren, and refusal rule below is therefore a genuine two-implementation
// question, and the per-backend suites at dolt and embeddeddolt could never
// answer it.

// LifecycleCloseReopenFixture supplies adapter-specific storage access for the
// Close and Reopen assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from, so a wiring is kit plus
// accessor plus prefix with no adapter in between.
type LifecycleCloseReopenFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Lifecycle is the role under test, reached through the backend's
	// capability accessor rather than a constructor.
	Lifecycle publicops.Lifecycle
	// CreateIssue seeds a durable issue in the issues plane, including its
	// labels.
	CreateIssue func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge and records a dependency_added event, so
	// every event assertion below is a DELTA around the verb under test.
	AddDependency func(context.Context, *types.Dependency, string) error
	// SetConfig installs the custom-status vocabulary the configured-done-
	// category cases are read against.
	SetConfig   func(context.Context, string, string) error
	QueryScalar func(context.Context, string, []any, ...any) error
}

// lifecycleCloseReopenCustomStatuses is the vocabulary the category cases
// install. Both cases that need custom statuses write this SAME value, so the
// suite has no ordering coupling through a shared config key: whichever runs
// first, the other still finds both names installed.
const lifecycleCloseReopenCustomStatuses = "lcrtriage:active,lcrarchived:done"

const (
	lifecycleCloseReopenActiveStatus = types.Status("lcrtriage")
	lifecycleCloseReopenDoneStatus   = types.Status("lcrarchived")
)

// RunLifecycleCloseRefusalsCarryTheirTypesAndWriteNothing pins the two unforced
// Close refusals at the level a caller can act on: not merely "an error", but
// the typed identity the leaf names, carrying the count a CLI renders — and
// with the row and the event stream untouched.
//
// issueops/issueops.go:406-409 promises "An unforced close with open children
// returns CloseOpenChildrenError without mutation" and that "Force bypasses
// blocker and open-child policy"; errors.go:105-128 declares the sentinel pair
// and the struct's IssueID/OpenChildren fields and its Unwrap to
// ErrCloseOpenChildren. A sentinel-only assertion — which is all the two
// store-backed per-backend suites make, and all the unit-of-work suite made
// before this case — cannot tell a refusal that reports one open child from one
// that reports zero, and cannot tell a refusal that rolled back from one that
// stamped closed_at on the way out.
func RunLifecycleCloseRefusalsCarryTheirTypesAndWriteNothing(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	parent := fixture.IssuePrefix + "-lcr-refuse-parent"
	child := fixture.IssuePrefix + "-lcr-refuse-child"
	blocker := fixture.IssuePrefix + "-lcr-refuse-blocker"
	blocked := fixture.IssuePrefix + "-lcr-refuse-blocked"
	for _, id := range []string{parent, child, blocker, blocked} {
		lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	}
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, child, parent, types.DepParentChild)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, blocked, blocker, types.DepBlocks)

	// The open-child refusal names the issue and counts the children, and
	// unwraps to the sentinel a caller may match instead.
	before := lifecycleCloseReopenReadRow(t, ctx, fixture, parent)
	events := newLifecycleCloseReopenEventCounter(t, ctx, fixture, parent)
	var openChildren *publicops.CloseOpenChildrenError
	_, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent})
	if !errors.As(err, &openChildren) {
		t.Fatalf("unforced close of %s with an open child: err = %v, want *CloseOpenChildrenError", parent, err)
	}
	if openChildren.IssueID != parent {
		t.Errorf("refusal names issue %q, want %q", openChildren.IssueID, parent)
	}
	if openChildren.OpenChildren != 1 {
		t.Errorf("refusal reports %d open children, want 1", openChildren.OpenChildren)
	}
	if !errors.Is(err, publicops.ErrCloseOpenChildren) {
		t.Errorf("refusal %v does not match ErrCloseOpenChildren", err)
	}
	lifecycleCloseReopenAssertRow(t, ctx, fixture, parent, "after the open-child refusal", before)
	events.assertNoneAdded(t, "open-child refusal")

	// The live-direct-blocker refusal is the other half, and it is a plain
	// sentinel: errors.go:105-108 declares no struct for it.
	before = lifecycleCloseReopenReadRow(t, ctx, fixture, blocked)
	blockedEvents := newLifecycleCloseReopenEventCounter(t, ctx, fixture, blocked)
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: blocked}); !errors.Is(err, publicops.ErrCloseBlocked) {
		t.Fatalf("unforced close of %s with a live blocker: err = %v, want ErrCloseBlocked", blocked, err)
	}
	lifecycleCloseReopenAssertRow(t, ctx, fixture, blocked, "after the blocker refusal", before)
	blockedEvents.assertNoneAdded(t, "blocker refusal")

	// Force bypasses both, which is what makes the two refusals above policy
	// rather than some unrelated failure to close.
	for _, id := range []string{parent, blocked} {
		forced, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: id, Force: true})
		if err != nil {
			t.Fatalf("forced close of %s: %v", id, err)
		}
		if !forced.Changed || forced.Issue.Status != types.StatusClosed {
			t.Fatalf("forced close of %s = %#v, want a committed close", id, forced)
		}
	}
}

// RunLifecycleCloseAdmitsATransitivelyBlockedTarget pins the adjective in the
// blocker refusal. The leaf says a crossing "with a LIVE DIRECT blocker returns
// ErrCloseBlocked" (issueops/issueops.go:415-416), and the shared store body
// spells the predicate out — blocked && len(blockers) > 0, refusing only when
// the denormalized is_blocked column is set AND at least one live direct
// blocker exists (internal/storage/issueops/close.go:44-54).
//
// The case above seeds a DIRECT blocker, so it passes just as well against a
// guard that refuses on the bare column. This one seeds the other half: a
// parent-child child of a blocked parent carries is_blocked = 1 with no direct
// blocker of its own, and closes unforced. That is the historical `bd close`
// behavior, and the same seeding is what a stale is_blocked column looks like
// after its blockers close — a guard reading the column instead of the live
// list makes both unclosable without Force.
//
// The two raw-row preconditions are load-bearing. Without the is_blocked read
// the case passes on a backend that never denormalizes transitively, and
// without the direct-edge count it passes on one that seeded no block at all;
// either way it would be asserting nothing. The control at the end is the third
// leg: it fails if the refusal was deleted outright rather than narrowed.
func RunLifecycleCloseAdmitsATransitivelyBlockedTarget(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-lcr-transitive-blocker"
	parent := fixture.IssuePrefix + "-lcr-transitive-parent"
	child := fixture.IssuePrefix + "-lcr-transitive-child"
	for _, id := range []string{blocker, parent, child} {
		lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	}
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, parent, blocker, types.DepBlocks)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, child, parent, types.DepParentChild)

	if got := lifecycleCloseReopenIsBlocked(t, ctx, fixture, child); got != 1 {
		t.Fatalf("%s is_blocked = %d, want 1: the case needs the transitive block the parent's blocker propagates", child, got)
	}
	if got := lifecycleCloseReopenDirectBlockerEdges(t, ctx, fixture, child); got != 0 {
		t.Fatalf("%s carries %d direct blocks edges, want 0: the whole point is a blocked row with no blocker of its own", child, got)
	}
	if got := lifecycleCloseReopenDirectBlockerEdges(t, ctx, fixture, parent); got != 1 {
		t.Fatalf("%s carries %d direct blocks edges, want the 1 this case seeded", parent, got)
	}

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: child})
	if err != nil {
		t.Fatalf("unforced close of transitively blocked %s: err = %v, want it to close — the refusal answers to a LIVE DIRECT blocker", child, err)
	}
	if !closed.Changed || closed.Issue.Status != types.StatusClosed {
		t.Fatalf("unforced close of %s = %#v, want a committed close", child, closed)
	}
	if row := lifecycleCloseReopenReadRow(t, ctx, fixture, child); types.Status(row.Status) != types.StatusClosed {
		t.Errorf("stored status for %s = %q, want %q", child, row.Status, types.StatusClosed)
	}

	// The control: the parent DOES hold a live direct blocker, and its only
	// child is now closed, so nothing else can be producing the refusal.
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent}); !errors.Is(err, publicops.ErrCloseBlocked) {
		t.Fatalf("unforced close of directly blocked %s: err = %v, want ErrCloseBlocked — the refusal must still be armed", parent, err)
	}
}

// lifecycleCloseReopenIsBlocked reads the denormalized blocked column. It is
// CAST to SIGNED because the three fixtures disagree on whether a TINYINT comes
// back as a number, a byte slice or a bool.
func lifecycleCloseReopenIsBlocked(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id string) int {
	t.Helper()
	var blocked int
	if err := fixture.QueryScalar(ctx,
		"SELECT CAST(COALESCE(is_blocked, 0) AS SIGNED) FROM issues WHERE id = ?", []any{id}, &blocked); err != nil {
		t.Fatalf("read is_blocked for %s: %v", id, err)
	}
	return blocked
}

// lifecycleCloseReopenDirectBlockerEdges counts the outgoing blocks edges that
// make a target's block DIRECT rather than inherited.
func lifecycleCloseReopenDirectBlockerEdges(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id string) int {
	t.Helper()
	var edges int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = ?",
		[]any{id, string(types.DepBlocks)}, &edges); err != nil {
		t.Fatalf("count direct blocker edges for %s: %v", id, err)
	}
	return edges
}

// RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose pins what a second Close
// of an already-closed issue does. issueops/issueops.go:354-359 promises
// Changed "is false for an idempotent re-close" and that OpenChildren "is
// reported even for an idempotent re-close"; :406-409 promises the unforced
// open-child refusal without qualifying it to open targets, and the shared
// store body reads the target's closed state precisely so a closed parent with
// open children still refuses (internal/storage/issueops/close.go:70-73).
//
// The attribution half — that the first close's reason, session, and closed_at
// survive a second one — is now stated outright by CloseRequest.Reason and
// CloseRequest.Session ("THE FIRST CLOSE WINS"), which also promise both
// values are read back on CloseResult.Issue. audit_issue-lifecycle.go:50-81
// pins the same rule at the STORE seam; this is the role seam, and the only
// place the two backends that never run that audit answer for it.
func RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lcr-idem"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)

	first, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: id, Reason: "first pass", Session: "session-one",
	})
	if err != nil {
		t.Fatalf("close %s: %v", id, err)
	}
	if !first.Changed {
		t.Fatalf("first close of %s reported Changed = false, want a committed close", id)
	}
	closedRow := lifecycleCloseReopenReadRow(t, ctx, fixture, id)
	if closedRow.CloseReason != "first pass" || closedRow.ClosedBySession != "session-one" {
		t.Fatalf("stored close attribution = (%q, %q), want (%q, %q)",
			closedRow.CloseReason, closedRow.ClosedBySession, "first pass", "session-one")
	}
	if closedRow.ClosedAt == "" {
		t.Fatalf("stored closed_at is empty after closing %s", id)
	}
	// The RESULT is the caller-visible half of the same clause: a caller that
	// never queries the row still reads the attribution it just wrote.
	if first.Issue.CloseReason != "first pass" || first.Issue.ClosedBySession != "session-one" {
		t.Errorf("CloseResult.Issue attribution = (%q, %q), want (%q, %q)",
			first.Issue.CloseReason, first.Issue.ClosedBySession, "first pass", "session-one")
	}
	if first.Issue.ClosedAt == nil {
		t.Errorf("CloseResult.Issue.ClosedAt is nil after closing %s, want the stamp the close wrote", id)
	}

	events := newLifecycleCloseReopenEventCounter(t, ctx, fixture, id)
	again, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: id, Reason: "second pass", Session: "session-two",
	})
	if err != nil {
		t.Fatalf("re-close %s: %v", id, err)
	}
	if again.Changed {
		t.Errorf("re-close of %s reported Changed = true, want false for an idempotent close", id)
	}
	if again.Issue.Status != types.StatusClosed {
		t.Errorf("re-close of %s reported status %q, want %q", id, again.Issue.Status, types.StatusClosed)
	}
	lifecycleCloseReopenAssertRow(t, ctx, fixture, id, "after the idempotent re-close", closedRow)
	events.assertNoneAdded(t, "idempotent re-close")

	// A reopen clears the pair, the other half of the same clause: they
	// describe a closure that no longer holds.
	if _, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: id}); err != nil {
		t.Fatalf("reopen %s: %v", id, err)
	}
	reopenedRow := lifecycleCloseReopenReadRow(t, ctx, fixture, id)
	if reopenedRow.CloseReason != "" || reopenedRow.ClosedBySession != "" {
		t.Errorf("close attribution after reopening %s = (%q, %q), want both cleared",
			id, reopenedRow.CloseReason, reopenedRow.ClosedBySession)
	}

	// A forced close of a parent with an open child reports the count, and so
	// does the forced re-close that changes nothing — while the UNFORCED
	// re-close of that same closed parent still refuses, because the policy
	// answers to the children, not to the target's own status.
	parent := fixture.IssuePrefix + "-lcr-idem-parent"
	child := fixture.IssuePrefix + "-lcr-idem-child"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, parent, types.StatusOpen, nil)
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, child, types.StatusOpen, nil)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, child, parent, types.DepParentChild)

	forced, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent, Force: true})
	if err != nil {
		t.Fatalf("forced close of %s: %v", parent, err)
	}
	if !forced.Changed || forced.OpenChildren != 1 {
		t.Fatalf("forced close of %s = (Changed %t, OpenChildren %d), want (true, 1)", parent, forced.Changed, forced.OpenChildren)
	}

	var openChildren *publicops.CloseOpenChildrenError
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent}); !errors.As(err, &openChildren) {
		t.Fatalf("unforced re-close of closed %s with an open child: err = %v, want *CloseOpenChildrenError", parent, err)
	}

	forcedAgain, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent, Force: true})
	if err != nil {
		t.Fatalf("forced re-close of %s: %v", parent, err)
	}
	if forcedAgain.Changed {
		t.Errorf("forced re-close of %s reported Changed = true, want false", parent)
	}
	if forcedAgain.OpenChildren != 1 {
		t.Errorf("forced re-close of %s reported %d open children, want 1 — the count is promised even when nothing moved", parent, forcedAgain.OpenChildren)
	}
}

// RunLifecycleReopenLeavesNonDoneStatusesUnchanged pins the Reopen no-op.
// issueops/issueops.go:411-413 promises Reopen "moves literal StatusClosed and
// configured done statuses to StatusOpen; non-done statuses unchanged", and
// :368-370 promises Changed "is false when non-done statuses are unchanged".
//
// "Non-done" is wider than "already open", and that is the whole point of the
// case: a wip built-in and a configured ACTIVE custom status are both left
// alone, and neither is the AlreadyOpen shape the shared body computes
// (internal/storage/issueops/reopen.go:47-49). The store bodies' own unit test
// covers the custom-status branch against sqlmock
// (internal/storage/issueops/reopen_test.go:82-195); this is the real-fixture,
// three-backend version, and the only one the unit-of-work backend runs.
//
// The configured-active leg needs a control, because an UNCONFIGURED status is
// also left alone and would pass this case for the wrong reason. Two things
// supply it: the readback below proves SetConfig landed, and
// RunLifecycleCloseAndReopenSpanTheConfiguredDoneCategory — same suite, same
// installed vocabulary, same const — proves this fixture's resolver reads that
// vocabulary, since an unresolved lcrarchived would fail it.
func RunLifecycleReopenLeavesNonDoneStatusesUnchanged(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	if err := fixture.SetConfig(ctx, "status.custom", lifecycleCloseReopenCustomStatuses); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}
	var installed string
	if err := fixture.QueryScalar(ctx, "SELECT value FROM config WHERE `key` = ?", []any{"status.custom"}, &installed); err != nil {
		t.Fatalf("read back status.custom: %v", err)
	}
	if installed != lifecycleCloseReopenCustomStatuses {
		t.Fatalf("installed status.custom = %q, want %q", installed, lifecycleCloseReopenCustomStatuses)
	}

	cases := []struct {
		name   string
		id     string
		status types.Status
	}{
		{name: "already open", id: fixture.IssuePrefix + "-lcr-noop-open", status: types.StatusOpen},
		{name: "built-in wip", id: fixture.IssuePrefix + "-lcr-noop-wip", status: types.StatusInProgress},
		{name: "configured active", id: fixture.IssuePrefix + "-lcr-noop-custom", status: lifecycleCloseReopenActiveStatus},
	}
	for _, tc := range cases {
		lifecycleCloseReopenSeedIssue(t, ctx, fixture, tc.id, tc.status, nil)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := lifecycleCloseReopenReadRow(t, ctx, fixture, tc.id)
			if types.Status(before.Status) != tc.status {
				t.Fatalf("seeded %s at status %q, want %q", tc.id, before.Status, tc.status)
			}
			events := newLifecycleCloseReopenEventCounter(t, ctx, fixture, tc.id)
			// A Reason rides along: a status the verb leaves alone records
			// nothing, Reason or not (issueops.go:310-313).
			result, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{
				Actor: "writer", IssueID: tc.id, Reason: "ignored",
			})
			if err != nil {
				t.Fatalf("reopen %s at %q: %v", tc.id, tc.status, err)
			}
			if result.Changed {
				t.Errorf("reopen of %s at %q reported Changed = true, want false", tc.id, tc.status)
			}
			if result.Issue.Status != tc.status {
				t.Errorf("reopen of %s reported status %q, want it unchanged at %q", tc.id, result.Issue.Status, tc.status)
			}
			lifecycleCloseReopenAssertRow(t, ctx, fixture, tc.id, "after the reopen no-op", before)
			events.assertNoneAdded(t, "reopen no-op")
		})
	}
}

// RunLifecycleCloseAndReopenSpanTheConfiguredDoneCategory pins that both verbs
// speak in terms of the configured done CATEGORY, not the literal closed
// status. issueops/issueops.go:403-405 promises Close "moves the issue to
// literal StatusClosed, including from a configured done status"; :370-372
// promises Reopen moves "literal StatusClosed and configured done statuses" to
// StatusOpen. Both are a real move — a caller that treated a configured done
// status as already-final would report Changed = false and leave the row on a
// status no built-in query matches.
func RunLifecycleCloseAndReopenSpanTheConfiguredDoneCategory(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	if err := fixture.SetConfig(ctx, "status.custom", lifecycleCloseReopenCustomStatuses); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}

	closing := fixture.IssuePrefix + "-lcr-done-close"
	reopening := fixture.IssuePrefix + "-lcr-done-reopen"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, closing, lifecycleCloseReopenDoneStatus, nil)
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, reopening, lifecycleCloseReopenDoneStatus, nil)

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: closing})
	if err != nil {
		t.Fatalf("close %s from configured done status %q: %v", closing, lifecycleCloseReopenDoneStatus, err)
	}
	if !closed.Changed {
		t.Errorf("close of %s from %q reported Changed = false, want a committed move to literal closed", closing, lifecycleCloseReopenDoneStatus)
	}
	if closed.Issue.Status != types.StatusClosed {
		t.Errorf("close of %s reported status %q, want %q", closing, closed.Issue.Status, types.StatusClosed)
	}
	if row := lifecycleCloseReopenReadRow(t, ctx, fixture, closing); types.Status(row.Status) != types.StatusClosed {
		t.Errorf("stored status for %s = %q, want %q", closing, row.Status, types.StatusClosed)
	}

	reopened, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: reopening})
	if err != nil {
		t.Fatalf("reopen %s from configured done status %q: %v", reopening, lifecycleCloseReopenDoneStatus, err)
	}
	if !reopened.Changed {
		t.Errorf("reopen of %s from %q reported Changed = false, want a committed move to open", reopening, lifecycleCloseReopenDoneStatus)
	}
	if reopened.Issue.Status != types.StatusOpen {
		t.Errorf("reopen of %s reported status %q, want %q", reopening, reopened.Issue.Status, types.StatusOpen)
	}
	if row := lifecycleCloseReopenReadRow(t, ctx, fixture, reopening); types.Status(row.Status) != types.StatusOpen {
		t.Errorf("stored status for %s = %q, want %q", reopening, row.Status, types.StatusOpen)
	}
}

// RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps pins the ORDERING clause
// both requests spell out. CloseRequest.ExpectedVersion "requires the current
// row version to match and is checked before an idempotent close"
// (issueops/issueops.go:295-300); ReopenRequest.ExpectedVersion is "checked
// before a non-done no-op" (:272-275); and Close's own doc adds "ExpectedVersion
// is checked first, including for an idempotent close", with Force bypassing
// "blocker and open-child policy" and nothing else (:364-365).
//
// The dangerous shape is not a stale version on a live mutation — it is a stale
// version on the request that would have done nothing anyway. An implementation
// that filters the no-op first answers a lost-update precondition with success,
// and the caller's compare-and-set silently stops fencing.
func RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	closedID := fixture.IssuePrefix + "-lcr-version-closed"
	openID := fixture.IssuePrefix + "-lcr-version-open"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, closedID, types.StatusOpen, nil)
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, openID, types.StatusOpen, nil)

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: closedID, Reason: "done"})
	if err != nil {
		t.Fatalf("close %s: %v", closedID, err)
	}

	// A stale version on a re-close that would have been a no-op. Force is the
	// interesting leg: it waives close policy, never this.
	stale := closed.Issue.RowVersion + 1
	before := lifecycleCloseReopenReadRow(t, ctx, fixture, closedID)
	for _, force := range []bool{false, true} {
		events := newLifecycleCloseReopenEventCounter(t, ctx, fixture, closedID)
		_, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
			Actor: "writer", IssueID: closedID, ExpectedVersion: &stale, Force: force,
		})
		if !errors.Is(err, publicops.ErrVersionMismatch) {
			t.Fatalf("re-close of closed %s with a stale version (force = %t): err = %v, want ErrVersionMismatch", closedID, force, err)
		}
		lifecycleCloseReopenAssertRow(t, ctx, fixture, closedID, "after the stale-version re-close", before)
		events.assertNoneAdded(t, "stale-version re-close")
	}

	// The current version still closes idempotently, which is what makes the
	// two refusals above about the precondition rather than about re-closing.
	current := before.RowLock
	matched, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: closedID, ExpectedVersion: &current,
	})
	if err != nil {
		t.Fatalf("re-close of %s with the current version: %v", closedID, err)
	}
	if matched.Changed {
		t.Errorf("re-close of %s with the current version reported Changed = true, want false", closedID)
	}

	// The same ordering on the other verb: a stale version on a reopen that
	// would have been a non-done no-op.
	openRow := lifecycleCloseReopenReadRow(t, ctx, fixture, openID)
	staleOpen := openRow.RowLock + 1
	events := newLifecycleCloseReopenEventCounter(t, ctx, fixture, openID)
	if _, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{
		Actor: "writer", IssueID: openID, ExpectedVersion: &staleOpen,
	}); !errors.Is(err, publicops.ErrVersionMismatch) {
		t.Fatalf("reopen of open %s with a stale version: err = %v, want ErrVersionMismatch", openID, err)
	}
	lifecycleCloseReopenAssertRow(t, ctx, fixture, openID, "after the stale-version reopen", openRow)
	events.assertNoneAdded(t, "stale-version reopen")

	currentOpen := openRow.RowLock
	noOp, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{
		Actor: "writer", IssueID: openID, ExpectedVersion: &currentOpen,
	})
	if err != nil {
		t.Fatalf("reopen of %s with the current version: %v", openID, err)
	}
	if noOp.Changed {
		t.Errorf("reopen of open %s with the current version reported Changed = true, want false", openID)
	}
}

// RunLifecycleReopenRecordsItsReason pins ReopenRequest.Reason: it "records
// why literal closed and configured done statuses move to open", and the leaf
// now says WHERE — the issue's event history, on the reopened entry the move
// itself records, and nowhere on the issue or the result.
//
// The no-reason leg is what makes the location a promise rather than a
// coincidence. It proves the reopened entry exists independently of the
// reason, so the first leg is reading a reason carried BY that entry rather
// than an entry minted because a reason was supplied.
func RunLifecycleReopenRecordsItsReason(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lcr-reason"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: id, Reason: "shipped"}); err != nil {
		t.Fatalf("close %s: %v", id, err)
	}

	reopened, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: id, Reason: "regressed"})
	if err != nil {
		t.Fatalf("reopen %s: %v", id, err)
	}
	if !reopened.Changed {
		t.Fatalf("reopen of closed %s reported Changed = false, want a committed reopen", id)
	}

	var recorded int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND COALESCE(new_value, '') = ?",
		[]any{id, string(types.EventReopened), "regressed"}, &recorded); err != nil {
		t.Fatalf("count recorded reopen reasons for %s: %v", id, err)
	}
	if recorded != 1 {
		t.Errorf("reopen of %s recorded %d reopened events carrying the reason, want 1", id, recorded)
	}

	quiet := fixture.IssuePrefix + "-lcr-reason-none"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, quiet, types.StatusOpen, nil)
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: quiet}); err != nil {
		t.Fatalf("close %s: %v", quiet, err)
	}
	if _, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: quiet}); err != nil {
		t.Fatalf("reopen %s with no reason: %v", quiet, err)
	}
	var entries, carrying int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		[]any{quiet, string(types.EventReopened)}, &entries); err != nil {
		t.Fatalf("count reopened events for %s: %v", quiet, err)
	}
	if entries != 1 {
		t.Errorf("reopen of %s with no reason recorded %d reopened events, want 1: the entry is the move's, not the reason's", quiet, entries)
	}
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND COALESCE(new_value, '') <> ''",
		[]any{quiet, string(types.EventReopened)}, &carrying); err != nil {
		t.Fatalf("count reopened events carrying a reason for %s: %v", quiet, err)
	}
	if carrying != 0 {
		t.Errorf("reopen of %s with no reason recorded %d reopened events carrying one, want 0", quiet, carrying)
	}
}

// RunLifecycleResultsAreHydratedPostStateSnapshots pins the shape both results
// promise: CloseResult.Issue and ReopenResult.Issue are "a detached post-state
// snapshot with labels and dependency records" (issueops/issueops.go:348-353,
// 362-367). A result that returned the bare row would leave a caller rendering
// an issue with no labels and no edges, and the two per-backend detachment
// tests only prove the snapshot does not ALIAS store state — not that it
// carries anything.
//
// Comments are the other half of that clause ("Comments are omitted") and are
// not asserted here: the frozen role fixture kit exposes no comment-seeding
// hook, so the assertion would pass on an empty comment table for the wrong
// reason. It belongs with a kit that can seed one.
func RunLifecycleResultsAreHydratedPostStateSnapshots(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	subject := fixture.IssuePrefix + "-lcr-snapshot"
	peer := fixture.IssuePrefix + "-lcr-snapshot-peer"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, subject, types.StatusOpen, []string{"lcr-tag"})
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, peer, types.StatusOpen, nil)
	// relates-to, not blocks: the edge has to be visible in the result without
	// making the subject unclosable.
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, subject, peer, types.DepRelatesTo)

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: subject})
	if err != nil {
		t.Fatalf("close %s: %v", subject, err)
	}
	lifecycleCloseReopenAssertSnapshot(t, "close result", closed.Issue, peer)

	reopened, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: subject})
	if err != nil {
		t.Fatalf("reopen %s: %v", subject, err)
	}
	lifecycleCloseReopenAssertSnapshot(t, "reopen result", reopened.Issue, peer)
}

// RunLifecycleCloseAndReopenRequireActorAndIssueID pins the deterministic
// validation floor both verbs sit on. The Lifecycle doc states "Deterministic
// request validation failures match ErrValidation" and "Refusals and
// deterministic validation failures leave persistent state unchanged"
// (issueops/issueops.go:375-376, 385-386); the shared store body spells the
// same two fields at internal/storage/issueops/execution.go:255-258, 281-284.
//
// The empty-Actor legs are the ones with teeth: the id names a real row, so an
// implementation that skipped the guard would close or reopen it.
func RunLifecycleCloseAndReopenRequireActorAndIssueID(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lcr-validation"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	before := lifecycleCloseReopenReadRow(t, ctx, fixture, id)

	closeCases := map[string]publicops.CloseRequest{
		"close without an actor":   {IssueID: id},
		"close without an issue":   {Actor: "writer"},
		"close without either":     {},
		"close with a blank actor": {Actor: "", IssueID: id, Reason: "ignored", Force: true},
	}
	for name, request := range closeCases {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.Lifecycle.Close(ctx, request); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("%s: err = %v, want ErrValidation", name, err)
			}
			lifecycleCloseReopenAssertRow(t, ctx, fixture, id, "after "+name, before)
		})
	}

	reopenCases := map[string]publicops.ReopenRequest{
		"reopen without an actor": {IssueID: id},
		"reopen without an issue": {Actor: "writer"},
		"reopen without either":   {},
	}
	for name, request := range reopenCases {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.Lifecycle.Reopen(ctx, request); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("%s: err = %v, want ErrValidation", name, err)
			}
			lifecycleCloseReopenAssertRow(t, ctx, fixture, id, "after "+name, before)
		})
	}
}

// RunLifecycleReopenProvenanceLabelsHistory pins both halves of
// ReopenRequest.Provenance (issueops/issueops.go:317-326): a spelled label is
// what the recorded entry reads, and the field "NEVER changes WHETHER history
// is recorded — only how the entry reads".
//
// The second half is the one worth a case. A caller reaching for Provenance is
// reaching for an entry it can find later, and an implementation that treated a
// label as a reason to record one would turn every no-op reopen into a commit
// naming work that did not happen.
//
// What this deliberately does NOT assert is the DEFAULT spelling. The clause
// says outright that the implementations disagree on it — the store-backed ones
// write "bd: reopen issue" and the unit-of-work one "reopen issue" — so pinning
// either would be asserting a promise the doc declines to make.
func RunLifecycleReopenProvenanceLabelsHistory(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	const label = "conformance: reopen provenance label"

	id := fixture.IssuePrefix + "-lcr-prov"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: id, Reason: "shipped"}); err != nil {
		t.Fatalf("close %s: %v", id, err)
	}

	before := lifecycleCloseReopenCountHistory(t, ctx, fixture, label)

	reopened, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: id, Reason: "regressed", Provenance: label})
	if err != nil {
		t.Fatalf("reopen %s: %v", id, err)
	}
	if !reopened.Changed {
		t.Fatalf("reopen of closed %s reported Changed = false, want a committed reopen", id)
	}
	if got := lifecycleCloseReopenCountHistory(t, ctx, fixture, label); got != before+1 {
		t.Errorf("reopen of %s left %d history entries reading %q, want %d", id, got, label, before+1)
	}

	// The issue is open now, so this reopen is a no-op. A label must not be
	// enough on its own to make an entry appear.
	noop, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: id, Reason: "again", Provenance: label})
	if err != nil {
		t.Fatalf("no-op reopen %s: %v", id, err)
	}
	if noop.Changed {
		t.Fatalf("reopen of already-open %s reported Changed = true, want a no-op", id)
	}
	if got := lifecycleCloseReopenCountHistory(t, ctx, fixture, label); got != before+1 {
		t.Errorf("no-op reopen of %s left %d history entries reading %q, want the %d the committed reopen wrote", id, got, label, before+1)
	}
}

// SPEC-GAP bd-yby99.31: which PLANE a Reopen id resolves against. The verb is
// documented entirely in terms of status (issueops/issueops.go:417-420) and
// names no plane, while every neighboring role states its answer outright —
// BatchCloser admits a wisp id "exactly as Lifecycle.Close resolves one"
// (issueops/batchcloser.go:35-36) and Claimer refuses one (claimer.go:63). All
// three implementations do resolve both planes today and both `bd reopen`
// routes now depend on it, but no assertion is written here: the doc makes no
// promise to assert against, and inventing one is what bd-yby99's policy
// forbids. The case seeds through the fixture kit's CreateWisp hook the day the
// clause exists.

// lifecycleCloseReopenCountHistory counts version-control entries carrying an
// EXACT message, which is the only way to tell the caller's spelling from the
// implementation's default. It is read as a delta by every caller: these
// fixtures share a database with their sibling cases, so an absolute count
// would carry their commits too.
func lifecycleCloseReopenCountHistory(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, message string) int {
	t.Helper()
	var count int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", []any{message}, &count); err != nil {
		t.Fatalf("count history entries reading %q: %v", message, err)
	}
	return count
}

// lifecycleCloseReopenRow is the stored close-lifecycle state one assertion
// compares before and after. row_lock is read as an int64 rather than a string
// because the version cases hand it straight back as an ExpectedVersion.
type lifecycleCloseReopenRow struct {
	Status          string
	RowLock         int64
	ClosedAt        string
	CloseReason     string
	ClosedBySession string
}

func lifecycleCloseReopenReadRow(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id string) lifecycleCloseReopenRow {
	t.Helper()
	var row lifecycleCloseReopenRow
	if err := fixture.QueryScalar(ctx,
		"SELECT status, row_lock, COALESCE(CAST(closed_at AS CHAR), ''), COALESCE(close_reason, ''), COALESCE(closed_by_session, '') FROM issues WHERE id = ?",
		[]any{id}, &row.Status, &row.RowLock, &row.ClosedAt, &row.CloseReason, &row.ClosedBySession); err != nil {
		t.Fatalf("read close-lifecycle row for %s: %v", id, err)
	}
	return row
}

// lifecycleCloseReopenAssertRow checks that the whole close-lifecycle row is
// where a previous read left it. Refusals and no-ops assert against it, so it
// covers row_lock too: a rewritten version is a lifecycle write that happened,
// even when status came out the same.
func lifecycleCloseReopenAssertRow(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id, label string, want lifecycleCloseReopenRow) {
	t.Helper()
	if got := lifecycleCloseReopenReadRow(t, ctx, fixture, id); got != want {
		t.Errorf("%s %s row = %+v, want it unchanged at %+v", id, label, got, want)
	}
}

// lifecycleCloseReopenAssertSnapshot checks a result issue carries the hydrated
// state the result doc promises.
func lifecycleCloseReopenAssertSnapshot(t *testing.T, label string, issue *types.Issue, wantEdgeTarget string) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s issue = nil, want a post-state snapshot", label)
	}
	found := false
	for _, name := range issue.Labels {
		if name == "lcr-tag" {
			found = true
		}
	}
	if !found {
		t.Errorf("%s labels = %v, want the seeded label", label, issue.Labels)
	}
	for _, dependency := range issue.Dependencies {
		if dependency != nil && dependency.DependsOnID == wantEdgeTarget {
			return
		}
	}
	t.Errorf("%s dependencies = %v, want a record naming %s", label, issue.Dependencies, wantEdgeTarget)
}

func lifecycleCloseReopenSeedIssue(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id string, status types.Status, labels []string) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: status, Priority: 2, IssueType: types.TypeTask, Labels: labels,
	}, "seed"); err != nil {
		t.Fatalf("seed %s at status %q: %v", id, status, err)
	}
}

func lifecycleCloseReopenSeedEdge(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, from, to string, kind types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: from, DependsOnID: to, Type: kind,
	}, "seed"); err != nil {
		t.Fatalf("seed %s %s -> %s: %v", kind, from, to, err)
	}
}

// lifecycleCloseReopenEventCounter reports how many event rows one issue gained
// across the operation under test. It is a DELTA, not an absolute: the fixture's
// AddDependency seed records a dependency_added event of its own.
type lifecycleCloseReopenEventCounter struct {
	ctx     context.Context
	fixture LifecycleCloseReopenFixture
	id      string
	total   int
}

func newLifecycleCloseReopenEventCounter(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id string) *lifecycleCloseReopenEventCounter {
	t.Helper()
	counter := &lifecycleCloseReopenEventCounter{ctx: ctx, fixture: fixture, id: id}
	counter.total = counter.count(t)
	return counter
}

func (c *lifecycleCloseReopenEventCounter) count(t *testing.T) int {
	t.Helper()
	var got int
	if err := c.fixture.QueryScalar(c.ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", []any{c.id}, &got); err != nil {
		t.Fatalf("count events for %s: %v", c.id, err)
	}
	return got
}

// assertNoneAdded checks that nothing landed since the previous baseline, and
// re-baselines. Zero is the only count this file ever asserts: every operation
// it counts around is a refusal or a no-op, and the promise in both cases is
// that the event stream did not move.
func (c *lifecycleCloseReopenEventCounter) assertNoneAdded(t *testing.T, label string) {
	t.Helper()
	total := c.count(t)
	if got := total - c.total; got != 0 {
		t.Errorf("%s wrote %d event rows for %s, want none", label, got, c.id)
	}
	c.total = total
}
