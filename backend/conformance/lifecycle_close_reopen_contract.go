package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

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
	// CreateWisp seeds an ephemeral issue in the wisps plane. Close policy
	// counts children in BOTH planes, so a case that seeds only durable ones
	// passes against an implementation that looks in one.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge and records a dependency_added event, so
	// every event assertion below is a DELTA around the verb under test. The
	// edge is routed to the plane its SOURCE lives in, which is how a wisp
	// child's parent-child edge reaches wisp_dependencies.
	AddDependency func(context.Context, *types.Dependency, string) error
	// SetConfig installs the custom-status vocabulary the configured-done-
	// category cases are read against.
	SetConfig   func(context.Context, string, string) error
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistoryMatching counts the history entries whose message matches a
	// SQL LIKE pattern ("" = every entry). Only the provenance case needs it,
	// and it needs the message rather than a bare count: the clause it pins is
	// that the recorded entry READS as the caller's own string.
	//
	// A nil CountHistoryMatching means "this backend cannot observe history by
	// message", and that case SKIPS loudly with that reason rather than
	// passing quietly. See history_matching.go for the convention.
	CountHistoryMatching func(context.Context, string) (int, error)
	// Exec runs a raw seeding script as ONE session, out of band of the role.
	//
	// It exists because the close policy answers to states no supported verb
	// can produce: a stale is_blocked column (every in-process close and every
	// blocker close recomputes it), and one child edge resident in both
	// dependency tables (a post-promotion or hand-resolved-merge artifact). It
	// is DELIBERATELY not on the frozen role fixture kit — it is this role's
	// out-of-band hook, built at each wiring site over a seam the backend
	// already publishes, the way CycleDetectorFixture.Exec is.
	//
	// The script is a slice rather than a statement because a foreign_key_checks
	// toggle and the insert it was for must land in one session.
	//
	// A nil Exec means "this backend cannot be given the state", and every case
	// that needs one SKIPS loudly with that reason rather than passing quietly.
	Exec func(ctx context.Context, statements []SQLStatement) error
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
// issueops/issueops.go:424-427 promises "An unforced close with open children
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

// RunLifecycleCloseCountsOpenChildrenInBothPlanes pins the SHAPE of the count
// behind the open-child refusal. issueops/issueops.go:424-427 promises the
// unforced refusal and that Force "reports OpenChildren", and CloseResult's own
// doc calls it "the number of open children observed" — a number, not a
// per-plane number, so an ephemeral child is one of them.
//
// The shared body counts twice and adds: once over `dependencies` joined to
// `issues`, once over `wisp_dependencies` joined to `wisps`, with the second
// query excluding any edge id the first table already holds
// (internal/storage/issueops/close.go:193-231). Every existing case in this
// file seeds durable children only, so BOTH of those halves are unpinned: an
// implementation that dropped the wisp query would refuse and count 1 where the
// promise is 2, and one that dropped the NOT EXISTS would count a
// dual-plane-resident edge twice.
//
// The dual-resident arm needs Exec because no supported verb produces it: the
// create-only guard spans both planes, so an ID cannot be made to hold a row in
// each. It is what a promotion or a hand-resolved merge leaves behind, and it
// is the state where a double count turns a legitimate close into a permanent
// refusal naming a child that does not exist twice.
func RunLifecycleCloseCountsOpenChildrenInBothPlanes(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	parent := fixture.IssuePrefix + "-lcr-planes-parent"
	durableChild := fixture.IssuePrefix + "-lcr-planes-durable-child"
	wispChild := fixture.IssuePrefix + "-lcr-planes-wisp-child"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, parent, types.StatusOpen, nil)
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, durableChild, types.StatusOpen, nil)
	lifecycleCloseReopenSeedWisp(t, ctx, fixture, wispChild)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, durableChild, parent, types.DepParentChild)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, wispChild, parent, types.DepParentChild)

	// The preconditions are load-bearing twice over: without them the case
	// passes on a backend that filed both edges in one table (where counting
	// one table would still reach 2), and it passes on one where the wisp seed
	// silently landed in the issues plane.
	if got := lifecycleCloseReopenChildEdges(t, ctx, fixture, "dependencies", parent); got != 1 {
		t.Fatalf("%s carries %d durable parent-child edges, want the 1 this case seeded", parent, got)
	}
	if got := lifecycleCloseReopenChildEdges(t, ctx, fixture, "wisp_dependencies", parent); got != 1 {
		t.Fatalf("%s carries %d ephemeral parent-child edges, want the 1 this case seeded: the count has to have a second plane to look in", parent, got)
	}

	var openChildren *publicops.CloseOpenChildrenError
	_, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent})
	if !errors.As(err, &openChildren) {
		t.Fatalf("unforced close of %s with a child in each plane: err = %v, want *CloseOpenChildrenError", parent, err)
	}
	if openChildren.OpenChildren != 2 {
		t.Errorf("refusal reports %d open children, want 2 — one durable and one ephemeral", openChildren.OpenChildren)
	}
	forced, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent, Force: true})
	if err != nil {
		t.Fatalf("forced close of %s: %v", parent, err)
	}
	if !forced.Changed || forced.OpenChildren != 2 {
		t.Errorf("forced close of %s = (Changed %t, OpenChildren %d), want (true, 2)", parent, forced.Changed, forced.OpenChildren)
	}

	// The other half: ONE edge resident in both dependency tables is one child.
	if fixture.Exec == nil {
		t.Skip("fixture cannot seed a dual-plane-resident child edge: Exec is nil, and no supported verb produces one")
	}
	dualParent := fixture.IssuePrefix + "-lcr-planes-dual-parent"
	dualChild := fixture.IssuePrefix + "-lcr-planes-dual-child"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, dualParent, types.StatusOpen, nil)
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, dualChild, types.StatusOpen, nil)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, dualChild, dualParent, types.DepParentChild)

	// The SAME edge id in both tables is what the exclusion keys on, so it is
	// read back rather than guessed: an id this case invented would leave the
	// NOT EXISTS matching nothing and the case passing for the wrong reason.
	var edgeID string
	if err := fixture.QueryScalar(ctx,
		"SELECT id FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = ?",
		[]any{dualChild, dualParent, string(types.DepParentChild)}, &edgeID); err != nil {
		t.Fatalf("read the durable edge id for %s: %v", dualChild, err)
	}
	if err := fixture.Exec(ctx, []SQLStatement{
		{Query: "INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, no_history) " +
			"VALUES (?, ?, '', '', '', '', ?, 2, ?, ?, ?)",
			Args: []any{dualChild, "ephemeral twin of " + dualChild, string(types.StatusOpen), string(types.TypeTask), true, false}},
		{Query: "INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (?, ?, ?, ?, NOW(), 'seed')",
			Args: []any{edgeID, dualChild, dualParent, string(types.DepParentChild)}},
	}); err != nil {
		t.Fatalf("seed the dual-plane-resident child edge: %v", err)
	}

	openChildren = nil
	_, err = fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: dualParent})
	if !errors.As(err, &openChildren) {
		t.Fatalf("unforced close of %s with one dual-resident child: err = %v, want *CloseOpenChildrenError", dualParent, err)
	}
	if openChildren.OpenChildren != 1 {
		t.Errorf("refusal reports %d open children for ONE child edge resident in both planes, want 1", openChildren.OpenChildren)
	}
}

// RunLifecycleCloseAdmitsAStaleBlockFlagWhoseBlockersHaveClosed pins the LIVE
// half of the blocker predicate. The shared body refuses on
// `blocked && len(blockers) > 0`, and it builds that blocker list by dropping
// every edge whose target is already closed or pinned
// (internal/storage/issueops/dependency_queries.go:970-984). Reading the live
// list rather than the denormalized column is what makes a stale is_blocked
// SELF-HEALING instead of a permanent refusal: an issue whose only blocker
// closed out of band would otherwise never close again without Force.
//
// RunLifecycleCloseAdmitsATransitivelyBlockedTarget is the neighboring case
// and it does NOT cover this: its target carries no direct edge at all, so it
// passes against an implementation whose blocker list is a plain edge list. The
// discriminating state is a live EDGE pointing at a CLOSED row, and it needs
// Exec — closing the blocker through any supported verb recomputes the
// depender's column back to 0 and takes the whole premise with it.
func RunLifecycleCloseAdmitsAStaleBlockFlagWhoseBlockersHaveClosed(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	if fixture.Exec == nil {
		t.Skip("fixture cannot seed a stale is_blocked column: Exec is nil, and every supported close recomputes it")
	}

	blocker := fixture.IssuePrefix + "-lcr-stale-blocker"
	target := fixture.IssuePrefix + "-lcr-stale-target"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, blocker, types.StatusOpen, nil)
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, target, types.StatusOpen, nil)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, target, blocker, types.DepBlocks)

	if got := lifecycleCloseReopenIsBlocked(t, ctx, fixture, target); got != 1 {
		t.Fatalf("%s is_blocked = %d, want the 1 the seeded blocks edge sets", target, got)
	}

	// Close the blocker with a raw write, which is what leaves the column
	// behind: the recompute every close runs is exactly what this case has to
	// skip.
	if err := fixture.Exec(ctx, []SQLStatement{
		{Query: "UPDATE issues SET status = ? WHERE id = ?", Args: []any{string(types.StatusClosed), blocker}},
	}); err != nil {
		t.Fatalf("raw-close the blocker: %v", err)
	}
	if got := lifecycleCloseReopenIsBlocked(t, ctx, fixture, target); got != 1 {
		t.Fatalf("%s is_blocked = %d after the raw blocker close, want it still reading a stale 1", target, got)
	}
	if got := lifecycleCloseReopenDirectBlockerEdges(t, ctx, fixture, target); got != 1 {
		t.Fatalf("%s carries %d direct blocks edges, want the 1 this case seeded — the edge is what makes it different from a transitive block", target, got)
	}

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: target})
	if err != nil {
		t.Fatalf("unforced close of %s whose only direct blocker is closed: err = %v, want it to close — the refusal answers to a LIVE blocker, not to the stored flag", target, err)
	}
	if !closed.Changed || closed.Issue.Status != types.StatusClosed {
		t.Fatalf("unforced close of %s = %#v, want a committed close", target, closed)
	}
}

// RunLifecycleCloseIsIdempotentOnAClosedRowThatStillLooksBlocked pins the OPEN
// half of the same predicate: the blocker refusal answers to a target that is
// not already closed (internal/storage/issueops/close.go:153).
//
// The leaf states the rule from the other side. CloseRequest.Reason says "A
// second Close of an already-closed issue is the no-op CloseResult.Changed
// describes", and Close's own doc enumerates the two things that can still
// refuse one — ExpectedVersion, "checked first, including for an idempotent
// close", and open children (issueops/issueops.go:423-425). A blocker
// is not on that list, so a re-close that met one would be a refusal the
// contract does not permit.
//
// The state is reachable but NOT from a forced close alone, which is why the
// body below restores the column by hand. A close settles the row it closes:
// the closed row is in its own affected set and a closed row is never blocked,
// so the flag comes down with the status
// (RunLifecycleCloseSettlesTheClosedRowItselfAndItsChild pins exactly that).
// What leaves a CLOSED row reading blocked is the invariant's merge clause — a
// pull that merges a forced close against a clone that still reads the row as
// blocked — and re-running the guard there would make that row permanently
// un-re-closable.
//
// RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose does not cover it: its row
// has no blocker, so `blocked` is false and the guard cannot fire whether or
// not it runs.
func RunLifecycleCloseIsIdempotentOnAClosedRowThatStillLooksBlocked(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	if fixture.Exec == nil {
		t.Skip("fixture cannot restore the is_blocked column a close clears: Exec is nil")
	}

	blocker := fixture.IssuePrefix + "-lcr-closedblocked-blocker"
	target := fixture.IssuePrefix + "-lcr-closedblocked-target"
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, blocker, types.StatusOpen, nil)
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, target, types.StatusOpen, nil)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, target, blocker, types.DepBlocks)

	forced, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: target, Reason: "shipped anyway", Force: true,
	})
	if err != nil {
		t.Fatalf("forced close of blocked %s: %v", target, err)
	}
	if !forced.Changed {
		t.Fatalf("forced close of %s reported Changed = false, want a committed close", target)
	}

	// The close cleared the column on its way out, so it is put back: the
	// discriminating state is a CLOSED row that still reads blocked while its
	// blocker is still open, which is what a cross-clone merge of the two
	// writes leaves and what the guard would meet if it ran.
	if err := fixture.Exec(ctx, []SQLStatement{
		{Query: "UPDATE issues SET is_blocked = 1 WHERE id = ?", Args: []any{target}},
	}); err != nil {
		t.Fatalf("restore the is_blocked column on the closed row: %v", err)
	}
	if got := lifecycleCloseReopenIsBlocked(t, ctx, fixture, target); got != 1 {
		t.Fatalf("%s is_blocked = %d, want the 1 this case restored", target, got)
	}
	if got := lifecycleCloseReopenDirectBlockerEdges(t, ctx, fixture, target); got != 1 {
		t.Fatalf("%s carries %d direct blocks edges, want the 1 this case seeded", target, got)
	}
	var blockerStatus string
	if err := fixture.QueryScalar(ctx, "SELECT status FROM issues WHERE id = ?", []any{blocker}, &blockerStatus); err != nil {
		t.Fatalf("read the blocker's status: %v", err)
	}
	if types.Status(blockerStatus) != types.StatusOpen {
		t.Fatalf("blocker %s status = %q, want it still open — a closed blocker would make the guard inert for the other reason", blocker, blockerStatus)
	}

	before := lifecycleCloseReopenReadRow(t, ctx, fixture, target)
	events := newLifecycleCloseReopenEventCounter(t, ctx, fixture, target)
	again, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: target, Reason: "second pass",
	})
	if err != nil {
		t.Fatalf("unforced re-close of closed-and-blocked %s: err = %v, want the promised no-op", target, err)
	}
	if again.Changed {
		t.Errorf("re-close of %s reported Changed = true, want false", target)
	}
	lifecycleCloseReopenAssertRow(t, ctx, fixture, target, "after the re-close of a closed blocked row", before)
	events.assertNoneAdded(t, "re-close of a closed blocked row")
}

// lifecycleCloseReopenChildEdges counts the parent-child edges naming id as the
// parent in ONE dependency table, so a case can prove its seed reached the
// plane it meant rather than assuming the routing.
func lifecycleCloseReopenChildEdges(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, table, id string) int {
	t.Helper()
	var edges int
	//nolint:gosec // G201: table is one of two literals chosen by the caller.
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM "+table+" WHERE depends_on_issue_id = ? AND type = ?",
		[]any{id, string(types.DepParentChild)}, &edges); err != nil {
		t.Fatalf("count %s parent-child edges into %s: %v", table, id, err)
	}
	return edges
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
// of an already-closed issue does. issueops/issueops.go:372-377 promises
// Changed "is false for an idempotent re-close" and that OpenChildren "is
// reported even for an idempotent re-close"; :424-425 promises the unforced
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

// RunLifecycleCloseAndReopenKeepTheClaimHolder pins the second half of the
// agent loop against the state the first half leaves: an in_progress row a named
// actor holds is CLOSEABLE, and the holder survives both the close and a later
// reopen.
//
// Every other close target in this package — here, in claim.go, in the audit
// files, in issue_operations_contract.go's close-policy seeds and in
// batch_closer_contract.go — is seeded OPEN and unassigned, so nothing pins the
// only close a working agent actually performs: the one on the row it is
// currently holding. Two divergences pass the whole suite today. A backend whose
// close admits open rows only refuses every claimed issue, so `bd claim` and
// `bd close` stop composing. A backend that clears assignee as part of the
// close — a defensible reading of "the work is over" — silently drops the
// attribution `bd show` renders and `bd list --assignee` filters on, and the
// reopen half loses it again for a caller handing the work back to the same
// holder.
//
// The reopen leg asserts the holder for a second reason: Reopen already clears
// the close attribution (CloseRequest.Reason "A Reopen clears both, because they
// describe a closure that no longer holds"), and assignee is the neighboring
// column that is NOT part of that closure record. An implementation sweeping the
// closure fields is exactly the one likely to take assignee with them.
func RunLifecycleCloseAndReopenKeepTheClaimHolder(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	const holder = "worker-1"
	id := fixture.IssuePrefix + "-lcr-held"
	lifecycleCloseReopenSeedClaimedIssue(t, ctx, fixture, id, holder)

	seeded := lifecycleCloseReopenReadRow(t, ctx, fixture, id)
	if types.Status(seeded.Status) != types.StatusInProgress || seeded.Assignee != holder {
		t.Fatalf("seeded %s as {status %q assignee %q}, want {%q %q} — the case has nothing to prove otherwise",
			id, seeded.Status, seeded.Assignee, types.StatusInProgress, holder)
	}

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: holder, IssueID: id, Reason: "work finished", Session: "session-held",
	})
	if err != nil {
		t.Fatalf("close %s while %s holds it: %v — an in_progress row is the one an agent actually closes", id, holder, err)
	}
	if !closed.Changed {
		t.Errorf("close of in_progress %s reported Changed = false, want a committed close", id)
	}
	if closed.Issue == nil {
		t.Fatalf("close of %s answered a nil Issue, want a post-state snapshot", id)
	}
	if closed.Issue.Status != types.StatusClosed {
		t.Errorf("CloseResult.Issue.Status = %q, want %q", closed.Issue.Status, types.StatusClosed)
	}
	if closed.Issue.Assignee != holder {
		t.Errorf("CloseResult.Issue.Assignee = %q, want %q — closing the work does not un-assign it", closed.Issue.Assignee, holder)
	}
	closedRow := lifecycleCloseReopenReadRow(t, ctx, fixture, id)
	if types.Status(closedRow.Status) != types.StatusClosed || closedRow.Assignee != holder {
		t.Errorf("stored row after the close = {status %q assignee %q}, want {%q %q}",
			closedRow.Status, closedRow.Assignee, types.StatusClosed, holder)
	}

	reopened, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: holder, IssueID: id, Reason: "regressed"})
	if err != nil {
		t.Fatalf("reopen %s: %v", id, err)
	}
	if !reopened.Changed {
		t.Errorf("reopen of closed %s reported Changed = false, want a committed reopen", id)
	}
	if reopened.Issue == nil {
		t.Fatalf("reopen of %s answered a nil Issue, want a post-state snapshot", id)
	}
	if reopened.Issue.Assignee != holder {
		t.Errorf("ReopenResult.Issue.Assignee = %q, want %q — the reopen clears the closure record, not the holder", reopened.Issue.Assignee, holder)
	}
	reopenedRow := lifecycleCloseReopenReadRow(t, ctx, fixture, id)
	if types.Status(reopenedRow.Status) != types.StatusOpen || reopenedRow.Assignee != holder {
		t.Errorf("stored row after the reopen = {status %q assignee %q}, want {%q %q}",
			reopenedRow.Status, reopenedRow.Assignee, types.StatusOpen, holder)
	}
	if reopenedRow.CloseReason != "" || reopenedRow.ClosedBySession != "" {
		t.Errorf("close attribution after reopening %s = (%q, %q), want both cleared — only the closure record goes",
			id, reopenedRow.CloseReason, reopenedRow.ClosedBySession)
	}
}

// RunLifecycleReopenLeavesNonDoneStatusesUnchanged pins the Reopen no-op.
// issueops/issueops.go:429-432 promises Reopen "moves literal StatusClosed and
// configured done statuses to StatusOpen; non-done statuses unchanged", and
// :386-387 promises Changed "is false when non-done statuses are unchanged".
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

	// The wip leg carries a HOLDER, because that is the state a live claim
	// leaves and the one a no-op reopen must not sweep: the whole row is
	// compared before and after, so an implementation that cleared assignee on
	// the way through fails here rather than passing on an empty column.
	cases := []struct {
		name     string
		id       string
		status   types.Status
		assignee string
	}{
		{name: "already open", id: fixture.IssuePrefix + "-lcr-noop-open", status: types.StatusOpen},
		{name: "built-in wip", id: fixture.IssuePrefix + "-lcr-noop-wip", status: types.StatusInProgress, assignee: "worker-noop"},
		{name: "configured active", id: fixture.IssuePrefix + "-lcr-noop-custom", status: lifecycleCloseReopenActiveStatus},
	}
	for _, tc := range cases {
		if tc.assignee != "" {
			lifecycleCloseReopenSeedClaimedIssue(t, ctx, fixture, tc.id, tc.assignee)
			continue
		}
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
			// nothing, Reason or not (issueops.go:322-330).
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
// status. issueops/issueops.go:422-423 promises Close "moves the issue to
// literal StatusClosed, including from a configured done status"; :430-431
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
//
// The case also pins the OTHER end of the same ordering, because the version
// check is early but not first: both verbs RESOLVE THE ISSUE before they judge
// the precondition, so a request naming an id that was never created reports
// ErrNotFound and not ErrVersionMismatch. The two answers send a caller
// somewhere different — a mismatch means "re-read and retry", a not-found means
// "this id is wrong" — and a body that read the version first would report the
// mismatch with every other case in this file still green, since nothing else
// here names a missing id at all.
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

	// The lookup comes FIRST. An id that names no row is ErrNotFound whether or
	// not the request carries a precondition, and it is never ErrVersionMismatch
	// — a caller that matched the mismatch would re-read and retry forever
	// against an id that will never exist.
	//
	// Both legs matter. Without a version the request reaches the plain resolve;
	// with one it reaches the precondition, which is the arm where an
	// implementation that reads row_lock before deciding the row exists reports
	// the wrong sentinel. The version supplied is arbitrary: a row that is not
	// there has no version for it to agree with.
	missingID := fixture.IssuePrefix + "-lcr-version-missing"
	arbitraryVersion := int64(1)
	for _, tc := range []struct {
		name    string
		version *int64
	}{
		{name: "unguarded"},
		{name: "guarded on a version", version: &arbitraryVersion},
	} {
		t.Run("missing id "+tc.name, func(t *testing.T) {
			_, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
				Actor: "writer", IssueID: missingID, ExpectedVersion: tc.version,
			})
			assertLifecycleMissingIDRefusal(t, "close", missingID, err)

			_, err = fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{
				Actor: "writer", IssueID: missingID, ExpectedVersion: tc.version,
			})
			assertLifecycleMissingIDRefusal(t, "reopen", missingID, err)

			// Neither refusal may have created the row it could not find.
			for _, table := range []string{"issues", "wisps"} {
				var rows int
				//nolint:gosec // G201: table is one of this file's own literals
				if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", []any{missingID}, &rows); err != nil {
					t.Fatalf("count %s rows for %s: %v", table, missingID, err)
				}
				if rows != 0 {
					t.Errorf("%s holds %d rows for the missing id %s, want none", table, rows, missingID)
				}
			}
		})
	}
}

// assertLifecycleMissingIDRefusal checks the sentinel a verb answers a missing
// id with. It asserts the NEGATIVE too: matching ErrNotFound is not enough if
// the error also matches ErrVersionMismatch, because a caller branching on the
// mismatch would take the retry path.
func assertLifecycleMissingIDRefusal(t *testing.T, verb, id string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s of the missing id %s: err = nil, want ErrNotFound", verb, id)
	}
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Errorf("%s of the missing id %s: err = %v, want ErrNotFound", verb, id, err)
	}
	if errors.Is(err, publicops.ErrVersionMismatch) {
		t.Errorf("%s of the missing id %s: err = %v, want it NOT to match ErrVersionMismatch — the id is wrong, not stale", verb, id, err)
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
// snapshot with labels and dependency records" (issueops/issueops.go:369-370,
// 383-384). A result that returned the bare row would leave a caller rendering
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
// (issueops/issueops.go:393-394, 403-404); the shared store body spells the
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
// ReopenRequest.Provenance (issueops/issueops.go:335-344): a spelled label is
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

	if fixture.CountHistoryMatching == nil {
		t.Skip("fixture has no CountHistoryMatching: this backend cannot observe history BY MESSAGE, so issueops.go:335-344 is UNPINNED here")
	}

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
// documented entirely in terms of status (issueops/issueops.go:429-432) and
// names no plane, while every neighboring role states its answer outright —
// BatchCloser admits a wisp id "exactly as Lifecycle.Close resolves one"
// (issueops/batchcloser.go:35-36) and Claimer refuses one (claimer.go:63). All
// three implementations do resolve both planes today and both `bd reopen`
// routes now depend on it, but no assertion is written here: the doc makes no
// promise to assert against, and inventing one is what bd-yby99's policy
// forbids. The case seeds through the fixture kit's CreateWisp hook the day the
// clause exists.

// RunLifecycleCloseSettlesItsTransitiveAndCrossPlaneDependers pins the
// local-write clause of issueops.BlockedStateInvariant on Close, which the
// existing cases in this file only ever read as a PRECONDITION.
//
// Close is where the affected set is widest, and the three subjects are the
// three ways it widens: the direct depender, that depender's parent-child child
// (which inherits and carries no blocker of its own), and a WISP depender whose
// edge lives in the ephemeral dependency table. A body that settled only the
// row the request named would pass a case watching the depender alone.
//
// Every subject's updated_at is asserted unchanged across the flip, which is
// the non-perturbation clause and can only be observed on a row that actually
// flipped: the mark and unmark templates never touch a row whose value stays
// put.
//
// WHAT IT DOES NOT COVER is the row the request names. Every subject here is
// downstream of the blocker and the blocker is itself unblocked, so the
// crossing row's own seat in the affected set belongs to
// RunLifecycleCloseSettlesTheClosedRowItselfAndItsChild.
//
// UNLIKE the two-body cases on DependencyEditor and Deleter, all three legs
// reach ONE body here (internal/storage/issueops.closeIssueInTx; the
// unit-of-work leg through its domain issue repository). This case is a
// wrapper and engine check, not a third vote, and blocked_state.go's header
// says so.
func RunLifecycleCloseSettlesItsTransitiveAndCrossPlaneDependers(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-bsclose-blocker"
	depender := fixture.IssuePrefix + "-bsclose-depender"
	child := fixture.IssuePrefix + "-bsclose-child"
	wispDepender := fixture.IssuePrefix + "-bsclose-wispdep"
	controlBlocker := fixture.IssuePrefix + "-bsclose-ctlblocker"
	controlDepender := fixture.IssuePrefix + "-bsclose-ctldepender"
	for _, id := range []string{blocker, depender, child, controlBlocker, controlDepender} {
		lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	}
	lifecycleCloseReopenSeedWisp(t, ctx, fixture, wispDepender)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, depender, blocker, types.DepBlocks)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, child, depender, types.DepParentChild)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, wispDepender, blocker, types.DepBlocks)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, controlDepender, controlBlocker, types.DepBlocks)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requirePlaneResidency(t, blockedWisp(wispDepender))
	probe.requirePlaneResidency(t, blockedIssue(blocker))
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker), "the direct depender")
	probe.requireBlockedByOpenBlocker(t, blockedWisp(wispDepender), blockedIssue(blocker), "the cross-plane depender")
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(child), "the child inherits its block and has none of its own")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(controlDepender), blockedIssue(controlBlocker), "the control's blocker is not the one being closed")

	flip := probe.watchFlip(t,
		[]blockedStateRow{blockedIssue(depender), blockedIssue(child), blockedWisp(wispDepender)},
		[]blockedStateRow{blockedIssue(controlDepender)})

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: blocker})
	if err != nil {
		t.Fatalf("close the blocker %s: %v", blocker, err)
	}
	if !closed.Changed {
		t.Fatalf("close of %s reported Changed = false, want a committed close", blocker)
	}

	flip.requireFlippedTo(t, 0,
		"closing a blocker settles its dependers, their descendants and both planes before the close commits")
}

// RunLifecycleCloseSettlesTheClosedRowItselfAndItsChild pins the clause of
// issueops.BlockedStateInvariant that every other is_blocked case in this
// package reads only from the far side: A ROW THAT IS CLOSED OR PINNED IS NEVER
// BLOCKED.
//
// The sibling case above, the reopen case below and the Update crossing case
// all arrange for the row whose status moves to be UNBLOCKED itself, and watch
// something downstream of it. That leaves the crossing row's OWN seat in the
// affected set unpinned — internal/storage/issueops/blocked_state.go's
// AffectedByStatusChangeInTx seeds the set with the id whose status changed,
// and deleting that seed leaves every one of those cases green. What it strands
// is not a transient: a blocked row that keeps is_blocked = 1 after it closes
// is an ORPHANED FLAG, because nothing downstream of a closed row will ever
// change its blockedness again and no verb will recompute it. The merge clause
// is the only place the invariant admits a stale column, and this is not a
// merge.
//
// THE CHILD IS THE OTHER HALF OF THE SAME SEED. The affected set expands by
// parent-child descendants FROM ITS SEEDS, and the depender load that reaches a
// neighbor follows blocks and conditional-blocks edges only — never
// parent-child. Drop the crossing row from the seed and its children go with
// it, silently, because no other case in this file hangs a child off the row
// being closed: the sibling's child hangs off the DEPENDER.
//
// WHY THE CLOSE IS FORCED. Both unforced refusals stand in the way here, and
// for opposite reasons — the subject is blocked (ErrCloseBlocked) and it has an
// open child (CloseOpenChildrenError). The child-shaped block that
// RunLifecycleCloseAdmitsATransitivelyBlockedTarget uses clears the first and
// not the second, and closing the child to clear the second would take the
// second subject with it, since a closed row cannot be blocked and so has
// nothing left to flip. Force waives close policy and nothing else —
// CloseRequest.Force "bypasses only blocker and open-child close policy" and
// "never bypasses validation, ExpectedVersion, or lifecycle rules"
// (issueops/issueops.go:310-311) — so it is the shape that keeps both subjects
// observable, and it is not exotic: it is the same forced close of a
// blocked issue that RunLifecycleCloseIsIdempotentOnAClosedRowThatStillLooksBlocked
// is built on.
//
// All three legs reach ONE body here (internal/storage/issueops.closeIssueInTx),
// so this is a wrapper and engine check rather than a third vote, exactly as
// blocked_state.go's header says of the whole Close family.
func RunLifecycleCloseSettlesTheClosedRowItselfAndItsChild(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-bsself-blocker"
	subject := fixture.IssuePrefix + "-bsself-subject"
	child := fixture.IssuePrefix + "-bsself-child"
	controlBlocker := fixture.IssuePrefix + "-bsself-ctlblocker"
	controlDepender := fixture.IssuePrefix + "-bsself-ctldepender"
	for _, id := range []string{blocker, subject, child, controlBlocker, controlDepender} {
		lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	}
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, subject, blocker, types.DepBlocks)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, child, subject, types.DepParentChild)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, controlDepender, controlBlocker, types.DepBlocks)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requireBlockedByOpenBlocker(t, blockedIssue(subject), blockedIssue(blocker),
		"the row whose status is about to cross is itself blocked, and by a live edge rather than a seeded column")
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(child),
		"the closed row's own child inherits the block and carries none of its own")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(controlDepender), blockedIssue(controlBlocker),
		"the control is blocked for a reason this close never reaches")

	// The subject is a flag SUBJECT and an updated_at exemption: the close
	// writes that row on purpose. The child is neither, so the non-perturbation
	// clause is observed on it.
	flip := probe.watchFlip(t,
		[]blockedStateRow{blockedIssue(subject), blockedIssue(child)},
		[]blockedStateRow{blockedIssue(controlDepender)}).
		alsoWrites(blockedIssue(subject))

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: "writer", IssueID: subject, Reason: "shipped anyway", Force: true,
	})
	if err != nil {
		t.Fatalf("forced close of blocked %s with an open child: %v", subject, err)
	}
	if !closed.Changed || closed.Issue.Status != types.StatusClosed {
		t.Fatalf("forced close of %s = %#v, want a committed close", subject, closed)
	}
	// The count is read for the FIXTURE, not for the policy: a child that was
	// already closed could not be blocked, and the second subject would be a row
	// with nothing to flip.
	if closed.OpenChildren != 1 {
		t.Fatalf("forced close of %s reports %d open children, want the 1 this case hung off it", subject, closed.OpenChildren)
	}
	if got := probe.rawStatus(t, blockedIssue(subject)); got != string(types.StatusClosed) {
		t.Fatalf("stored status for %s = %q, want %q: the rest of this case is about what a CLOSED status means for the flag",
			subject, got, types.StatusClosed)
	}
	// The blocker is still open, which is what makes the two flips below
	// attributable to the SUBJECT closing rather than to its cause going away —
	// the mechanism the sibling case already covers.
	if got := probe.rawStatus(t, blockedIssue(blocker)); got != string(types.StatusOpen) {
		t.Fatalf("blocker %s status = %q, want it still open", blocker, got)
	}

	flip.requireFlippedTo(t, 0,
		"a closed row is never blocked, and settling it settles its own parent-child child with it")
}

// RunLifecycleCloseOnASpawnersLastChildSatisfiesAWaitsForGate pins the third
// arm of the blocking predicate — "a waits-for edge whose gate over the
// spawner's children is not yet satisfied" — on the side of it that no case
// reached.
//
// THE ARM, BY NAME. internal/storage/issueops/blocked_state.go's
// AffectedByStatusChangeInTx builds the affected set from three loads, and one
// of them, loadWaitersWhoseSpawnerIsParentOfInTx, exists for exactly this
// shape: the row whose status moved is a CHILD, and the row that has to settle
// is a waiter on that child's PARENT — a row the other two loads cannot reach,
// because it holds no blocking edge onto the child (so the depender load misses
// it) and it waits on the spawner rather than on the child (so the waiter load
// misses it too). Delete that one call and this case is the only thing that
// goes red. The DependencyEditor's gate case covers the ADD side of a gate; it
// runs through AffectedByDepChangeInTx and never touches this load.
//
// WHAT THE FIXTURE MAKES OBSERVABLE, and it is the whole design: the spawner
// has EXACTLY ONE open child, asserted before the close in both planes. A
// spawner with a second open child leaves an all-children gate unsatisfied
// after the close, so the subject could not flip and the case would pass
// against a body that never recomputed it — an unfalsifiable case wearing a
// correct assertion, which is the defect this program already shipped once.
//
// TWO CONTROLS, one on each side of the affected set. edgedWaiter waits on the
// SAME spawner, so the same recompute visits it, and stays blocked because it
// also holds a blocks edge onto a live row: it separates "the gate was
// re-evaluated" from "the flag was cleared". otherWaiter is gated on a
// different spawner whose own child nobody touched, so it separates a correct
// affected set from a blanket pass over every waiter in the workspace.
//
// All three legs reach ONE body here (internal/storage/issueops.closeIssueInTx),
// so this is a wrapper and engine check rather than a third vote, exactly as
// blocked_state.go's header says of the whole Close family.
func RunLifecycleCloseOnASpawnersLastChildSatisfiesAWaitsForGate(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	spawner := fixture.IssuePrefix + "-bswait-spawner"
	lastChild := fixture.IssuePrefix + "-bswait-lastchild"
	waiter := fixture.IssuePrefix + "-bswait-waiter"
	edgedWaiter := fixture.IssuePrefix + "-bswait-edgedwaiter"
	edgeBlocker := fixture.IssuePrefix + "-bswait-edgeblocker"
	otherSpawner := fixture.IssuePrefix + "-bswait-otherspawner"
	otherChild := fixture.IssuePrefix + "-bswait-otherchild"
	otherWaiter := fixture.IssuePrefix + "-bswait-otherwaiter"
	for _, id := range []string{spawner, lastChild, waiter, edgedWaiter, edgeBlocker, otherSpawner, otherChild, otherWaiter} {
		lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	}

	// The hierarchies land FIRST. A gate over a spawner with no children at all
	// is already satisfied, so a waits-for edge seeded before its spawner had a
	// child would leave the waiter unblocked and give this case nothing to flip.
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, lastChild, spawner, types.DepParentChild)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, otherChild, otherSpawner, types.DepParentChild)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, edgedWaiter, edgeBlocker, types.DepBlocks)
	for _, edge := range []struct{ waiter, spawner string }{
		{waiter, spawner}, {edgedWaiter, spawner}, {otherWaiter, otherSpawner},
	} {
		lifecycleCloseReopenSeedWaitsForEdge(t, ctx, fixture, edge.waiter, edge.spawner, types.WaitsForAllChildren)
	}

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	// The trap this case is built around: one open child, so closing it is the
	// transition that satisfies the gate.
	assertLifecycleCloseReopenOpenChildCount(t, ctx, fixture, spawner, 1)
	assertLifecycleCloseReopenOpenChildCount(t, ctx, fixture, otherSpawner, 1)
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(waiter),
		"the subject's block is the GATE — the flag with no blocking edge of its own is what says so")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(edgedWaiter), blockedIssue(edgeBlocker),
		"the in-set control is blocked by a cause this close does not touch")
	probe.requireBlockedWithNoDirectBlockerEdges(t, blockedIssue(otherWaiter),
		"the out-of-set control is gated on a spawner whose child nobody closes")
	probe.requireUnblocked(t, blockedIssue(lastChild), "the row being closed carries no block of its own to confuse the flip with")

	flip := probe.watchFlip(t,
		[]blockedStateRow{blockedIssue(waiter)},
		[]blockedStateRow{blockedIssue(edgedWaiter), blockedIssue(otherWaiter)})

	closed, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: lastChild})
	if err != nil {
		t.Fatalf("close the spawner's last open child %s: %v", lastChild, err)
	}
	if !closed.Changed {
		t.Fatalf("close of %s reported Changed = false, want a committed close", lastChild)
	}
	assertLifecycleCloseReopenOpenChildCount(t, ctx, fixture, spawner, 0)

	flip.requireFlippedTo(t, 0,
		"closing a spawner's LAST open child satisfies an all-children gate, and the closing transaction settles the waiter")

	// The flip is attributable to the GATE and to nothing else: the spawner
	// itself never moved, and the waits-for edge that gated the waiter is still
	// there. A 0 read off a row whose edge had vanished would be a different
	// fact wearing the same value.
	if got := probe.rawStatus(t, blockedIssue(spawner)); got != string(types.StatusOpen) {
		t.Fatalf("spawner %s status = %q, want it still open: the waiter's flip must come from the gate, not from its target closing", spawner, got)
	}
	assertLifecycleCloseReopenWaitsForEdgeCount(t, ctx, fixture, waiter, spawner, 1)
}

// RunLifecycleReopenReblocksItsDependers is the other direction, and it is what
// makes the pair complete: the unmark template and the mark template are
// separate SQL, so a case that only ever watches a flag fall exercises one of
// them.
//
// The control is a depender whose blocker was CLOSED WHEN IT WAS SEEDED and
// never reopened. It has the same shape as the subject and the same zero, so it
// separates "reopening the blocker re-blocked this row" from "the verb re-marks
// whatever it can reach".
func RunLifecycleReopenReblocksItsDependers(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture) {
	t.Helper()

	blocker := fixture.IssuePrefix + "-bsreopen-blocker"
	depender := fixture.IssuePrefix + "-bsreopen-depender"
	wispDepender := fixture.IssuePrefix + "-bsreopen-wispdep"
	controlBlocker := fixture.IssuePrefix + "-bsreopen-ctlblocker"
	controlDepender := fixture.IssuePrefix + "-bsreopen-ctldepender"
	for _, id := range []string{blocker, depender, controlDepender} {
		lifecycleCloseReopenSeedIssue(t, ctx, fixture, id, types.StatusOpen, nil)
	}
	lifecycleCloseReopenSeedIssue(t, ctx, fixture, controlBlocker, types.StatusClosed, nil)
	lifecycleCloseReopenSeedWisp(t, ctx, fixture, wispDepender)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, depender, blocker, types.DepBlocks)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, wispDepender, blocker, types.DepBlocks)
	lifecycleCloseReopenSeedEdge(t, ctx, fixture, controlDepender, controlBlocker, types.DepBlocks)

	probe := newBlockedStateProbe(ctx, fixture.QueryScalar)
	probe.requirePlaneResidency(t, blockedWisp(wispDepender))
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker), "the pre-close state this case unwinds and rewinds")
	probe.requireUnblocked(t, blockedIssue(controlDepender), "the control's blocker was already closed when its edge landed")

	if _, err := fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: blocker}); err != nil {
		t.Fatalf("close the blocker %s to reach the reopen precondition: %v", blocker, err)
	}
	probe.requireUnblocked(t, blockedIssue(depender), "the close is what put the subject at 0 — earned, never seeded")
	probe.requireUnblocked(t, blockedWisp(wispDepender), "the cross-plane depender came down with it")

	flip := probe.watchFlip(t,
		[]blockedStateRow{blockedIssue(depender), blockedWisp(wispDepender)},
		[]blockedStateRow{blockedIssue(controlDepender)})

	reopened, err := fixture.Lifecycle.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: blocker})
	if err != nil {
		t.Fatalf("reopen the blocker %s: %v", blocker, err)
	}
	if !reopened.Changed {
		t.Fatalf("reopen of closed %s reported Changed = false, want a committed reopen", blocker)
	}

	flip.requireFlippedTo(t, 1, "a reopened blocker blocks again, on both planes, before the reopen commits")
	probe.requireBlockedByOpenBlocker(t, blockedIssue(depender), blockedIssue(blocker), "the postcondition is the flag AND the live blocker behind it")
}

// lifecycleCloseReopenCountHistory counts version-control entries carrying an
// EXACT message, which is the only way to tell the caller's spelling from the
// implementation's default. It is read as a delta by every caller: these
// fixtures share a database with their sibling cases, so an absolute count
// would carry their commits too.
func lifecycleCloseReopenCountHistory(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, message string) int {
	t.Helper()
	count, err := fixture.CountHistoryMatching(ctx, historyPatternForExactMessage(t, message))
	if err != nil {
		t.Fatalf("count history entries reading %q: %v", message, err)
	}
	return count
}

// lifecycleCloseReopenRow is the stored close-lifecycle state one assertion
// compares before and after. row_lock is read as an int64 rather than a string
// because the version cases hand it straight back as an ExpectedVersion.
//
// assignee is part of the row rather than a separate read because every refusal
// and no-op leg in this file asserts against the whole struct: a close that
// dropped the holder on a path that changed nothing else would otherwise be
// invisible here, and `bd show` and `bd list --assignee` would lose the holder
// after every replayed close.
type lifecycleCloseReopenRow struct {
	Status          string
	Assignee        string
	RowLock         int64
	ClosedAt        string
	CloseReason     string
	ClosedBySession string
}

func lifecycleCloseReopenReadRow(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id string) lifecycleCloseReopenRow {
	t.Helper()
	var row lifecycleCloseReopenRow
	if err := fixture.QueryScalar(ctx,
		"SELECT status, COALESCE(assignee, ''), row_lock, COALESCE(CAST(closed_at AS CHAR), ''), COALESCE(close_reason, ''), COALESCE(closed_by_session, '') FROM issues WHERE id = ?",
		[]any{id}, &row.Status, &row.Assignee, &row.RowLock, &row.ClosedAt, &row.CloseReason, &row.ClosedBySession); err != nil {
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

// lifecycleCloseReopenSeedClaimedIssue seeds the state a won claim leaves
// behind: an in_progress row a named actor holds. It seeds directly rather than
// claiming through the role because the close/reopen assertions answer to the
// STATE, and a fixture that had to reach it through a Claim would need the
// claim seam this file otherwise does not use.
func lifecycleCloseReopenSeedClaimedIssue(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id, assignee string) {
	t.Helper()
	started := time.Now().UTC()
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusInProgress, Priority: 2, IssueType: types.TypeTask,
		Assignee: assignee, StartedAt: &started,
	}, "seed"); err != nil {
		t.Fatalf("seed %s as claimed by %s: %v", id, assignee, err)
	}
}

func lifecycleCloseReopenSeedWisp(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, id string) {
	t.Helper()
	if fixture.CreateWisp == nil {
		t.Fatalf("seed wisp %s: fixture has no CreateWisp hook", id)
	}
	if err := fixture.CreateWisp(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
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

// lifecycleCloseReopenSeedWaitsForEdge seeds a waits-for edge carrying its GATE.
// It goes through the constructor rather than a literal because the gate lives
// in edge metadata whose spelling the derivation engine reads, and a hand-built
// JSON blob here would be this file's guess at that spelling.
func lifecycleCloseReopenSeedWaitsForEdge(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, waiter, spawner, gate string) {
	t.Helper()
	edge, err := types.NewWaitsForDependency(waiter, spawner, gate)
	if err != nil {
		t.Fatalf("build the %s waits-for edge %s -> %s: %v", gate, waiter, spawner, err)
	}
	if err := fixture.AddDependency(ctx, edge, "seed"); err != nil {
		t.Fatalf("seed the %s waits-for edge %s -> %s: %v", gate, waiter, spawner, err)
	}
}

// assertLifecycleCloseReopenOpenChildCount counts a spawner's parent-child
// children that are neither closed nor pinned, IN BOTH PLANES. The gate case
// reads it as a fixture check on both sides of the close: a spawner with a
// second open child would leave an all-children gate unsatisfied afterwards, so
// the subject could not flip and the case could not fail.
func assertLifecycleCloseReopenOpenChildCount(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, spawner string, want int) {
	t.Helper()
	var got int
	if err := fixture.QueryScalar(ctx, `
		SELECT (
		    SELECT COUNT(*) FROM dependencies d JOIN issues c ON c.id = d.issue_id
		    WHERE d.type = 'parent-child' AND d.depends_on_issue_id = ?
		      AND c.status <> 'closed' AND c.status <> 'pinned'
		  ) + (
		    SELECT COUNT(*) FROM wisp_dependencies d JOIN wisps c ON c.id = d.issue_id
		    WHERE d.type = 'parent-child' AND d.depends_on_issue_id = ?
		      AND c.status <> 'closed' AND c.status <> 'pinned'
		  )`, []any{spawner, spawner}, &got); err != nil {
		t.Fatalf("count the open children of %s: %v", spawner, err)
	}
	if got != want {
		t.Fatalf("%s has %d open children, want %d: an all-children gate case is about the LAST one closing", spawner, got, want)
	}
}

func assertLifecycleCloseReopenWaitsForEdgeCount(t *testing.T, ctx context.Context, fixture LifecycleCloseReopenFixture, waiter, spawner string, want int) {
	t.Helper()
	var got int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = ?",
		[]any{waiter, spawner, string(types.DepWaitsFor)}, &got); err != nil {
		t.Fatalf("count waits-for edges %s -> %s: %v", waiter, spawner, err)
	}
	if got != want {
		t.Errorf("waits-for edges %s -> %s = %d, want %d", waiter, spawner, got, want)
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
