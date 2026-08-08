package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.Claimer must satisfy. Each case asserts what issueops/claimer.go
// PROMISES rather than what any one backend happens to do today; a backend that
// disagrees is parked at its own wiring site with skipKnownDivergence so the
// case still runs on the ones that agree.
//
// THE VOTE COUNT. Three accessors answer the role — the server-backed store,
// the embedded store and the unit-of-work provider — and TWO independent bodies
// sit under them. The two stores share internal/storage/issueops.ExecuteClaim
// over ClaimIssueInTx; the unit of work composes domain.IssueUseCase.ClaimIssue
// over internal/storage/domain/db's issueSQLRepositoryImpl.Claim, which is a
// hand-maintained DUAL of that CAS. So a case passing three times is two
// readings plus an engine check, never three independent votes — and the dual
// is exactly where this family has drifted before (the row_lock rewrite and the
// custom-active source statuses each had to be back-ported into it by hand;
// both scars are commented in db/issue.go's Claim).
//
// WHY THIS CONTRACT SEEDS FIVE STATE CLASSES AND NOT ONE. Claim's eligibility
// is a conjunction of two predicates that a fixture seeding only
// open-and-unassigned rows collapses into one reachable arm:
//
//	the STATUS arm      built-in StatusOpen, or a CONFIGURED ACTIVE status
//	the HOLDER arm      unassigned, assigned to Actor, or a CONFIGURED POOL
//
// Both configured terms are read out of the workspace at claim time
// (ClaimableSourceStatusesInTx, ClaimPoolAliasesInTx). A suite that never wrote
// status.custom or claim.pools would be green over an implementation that had
// hardcoded `status = 'open'` and `assignee = '' OR assignee = ?` — which is
// what BOTH bodies used to say. The pool alias here is therefore never equal to
// the claiming actor, and the custom-status case always carries a
// wip-category DECOY, because a pool that happened to be the actor would pass
// through the assigned-to-Actor arm and a custom status with no category check
// would pass through "any custom status is claimable".
//
// THE THIRD HOLDER ARM IS ITS OWN CASE for the same reason: an OPEN issue
// already assigned to the Actor is a real mutation (Changed TRUE), not the
// idempotent re-claim, and an implementation that keyed idempotence on the
// assignee alone rather than on the assignee AND StatusInProgress answers
// Changed false and writes nothing. Only a case that seeds open-assigned-to-me
// can see that.
//
// STATE IS READ BACK AS RAW ROWS, never through the role. "Refusals and
// deterministic validation failures leave persistent state unchanged" is the
// clause a role-answer assertion cannot check: reading the row back through the
// thing under test is exactly the check that passes on a corrupted table.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN, and why:
//
//   - "Implementations own transaction retry: a claim that loses a commit-time
//     merge is retried, never surfaced." Not black-box observable from a single
//     threaded case, and a concurrency case would be flaky at three engines. It
//     is pinned by the SHAPE of the bodies (RunTxResult / withRetryTx) and by
//     review.
//   - "when the refusing state was readable in the same transaction". Only the
//     POSITIVE half is reachable here — a black-box case cannot make the
//     same-transaction re-read fail. The negative half (a bare refusal when the
//     read failed) is pinned by uow/issue_claimer_test.go's fake.
//   - "Implementations never mutate caller-owned request values." ClaimRequest
//     is two strings with no reference members, so there is nothing a callee
//     could write through. Structurally unobservable, not omitted by choice.
//   - "Result values are unspecified when error is non-nil", so no refusal case
//     asserts anything about the returned ClaimResult.
//   - "a detached post-state snapshot". Detachment is not observable across the
//     seam; what IS observable — that the snapshot equals the row left in the
//     database — is asserted instead.

// ClaimerFixture supplies adapter-specific storage access for the guarded-claim
// assertions. Every field is named and typed exactly like the per-backend
// roleFixtureKit hook it is filled from.
type ClaimerFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	Claimer     publicops.Claimer
	// CreateIssue seeds a durable issue in the issues plane. Cases seed
	// Assignee and Status directly through it: the holder and status arms of
	// the eligibility predicate are properties of the ROW, and there is no
	// front door on this role that reaches either.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane, for the one
	// clause that is about the plane rather than the row.
	CreateWisp func(context.Context, *types.Issue, string) error
	// SetConfig writes one workspace config key. It is how the two CONFIGURED
	// terms of the eligibility predicate — status.custom and claim.pools — get
	// installed; without it half this contract is unreachable.
	SetConfig func(context.Context, string, string) error
	// QueryScalar runs a single-row query and scans it, and RETURNS the error
	// rather than failing the test. It is how the cases read RAW ROWS — the
	// only way to tell "the answer looks right" from "the table is right".
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has,
	// for the idempotent re-claim's "nothing was committed". A nil hook means
	// this backend cannot observe history: the delta checks are then skipped
	// rather than passing on an arithmetic accident, and the case whose whole
	// subject is the clause says so. It is non-nil on all three fixtures today.
	CountHistory func(context.Context) (int, error)
}

// RunClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow pins the winning
// path of the compare-and-set in issueops/claimer.go's Claim doc — "sets
// Assignee to Actor and Status to StatusInProgress" — together with the two
// promises the result type makes about what comes back: ClaimResult.Changed is
// true for a persisted mutation, and ClaimResult.Issue is the issue ROW with
// labels, dependency records and comments omitted.
//
// THE ISSUE IS SEEDED WITH A LABEL, and the label's presence in the labels
// table is asserted first. That is what makes the bare-row half falsifiable at
// all: the shared body hydrates labels on its way past (GetIssueInTx) and then
// clears them deliberately, so a case seeding an unlabeled issue would be green
// over the clearing being deleted. The label row is checked separately because
// "the result carries no labels" and "the claim dropped the issue's labels" are
// two very different bugs with the same reading.
//
// Dependencies and Comments are asserted alongside Labels as cheap tripwires
// and nothing more: no read on this path ever populates either, on any of the
// three bodies, so those two lines cannot fail today. They are here so a future
// hydrator has something to trip over, and they are called out rather than left
// to look like coverage.
//
// THE CLAIMANT IS A STRANGER — an actor that appears nowhere in the fixture,
// neither as creator nor as assignee. That is the "any actor may claim" clause:
// the actor is caller-asserted provenance, not authenticated identity, and
// eligibility is decided by the issue's state alone.
func RunClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-clwin"
	label := fixture.IssuePrefix + "-clwin-label"
	issue := claimerIssue(id, types.StatusOpen)
	issue.Labels = []string{label}
	seedClaimerIssue(t, ctx, fixture, issue)
	if got := claimerLabelCount(t, ctx, fixture, id); got != 1 {
		t.Fatalf("the seeded issue carries %d label row(s), want 1; without one the bare-row assertion below cannot fail", got)
	}

	result, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: id})
	if err != nil {
		t.Fatalf("Claim of an open, unassigned issue by an actor with no prior relationship to it: %v", err)
	}
	if !result.Changed {
		t.Error("Changed = false for a claim that moved an open issue to in-progress")
	}
	if result.Issue == nil {
		t.Fatal("Claim returned a nil Issue on the winning path")
	}
	if result.Issue.Status != types.StatusInProgress {
		t.Errorf("returned status = %q, want %q", result.Issue.Status, types.StatusInProgress)
	}
	if result.Issue.Assignee != claimerStranger {
		t.Errorf("returned assignee = %q, want %q", result.Issue.Assignee, claimerStranger)
	}
	if result.Issue.Labels != nil {
		t.Errorf("returned Issue.Labels = %v, want nil — ClaimResult.Issue is the bare issue row", result.Issue.Labels)
	}
	if result.Issue.Dependencies != nil {
		t.Errorf("returned Issue.Dependencies = %v, want nil — ClaimResult.Issue is the bare issue row", result.Issue.Dependencies)
	}
	if result.Issue.Comments != nil {
		t.Errorf("returned Issue.Comments = %v, want nil — ClaimResult.Issue is the bare issue row", result.Issue.Comments)
	}

	// The snapshot IS the stored state, read as a raw row rather than back
	// through the role.
	assertClaimerRowState(t, ctx, fixture, id, string(result.Issue.Status), result.Issue.Assignee)
	assertClaimerRowState(t, ctx, fixture, id, string(types.StatusInProgress), claimerStranger)
	// The label the result omits is still on the issue.
	if got := claimerLabelCount(t, ctx, fixture, id); got != 1 {
		t.Errorf("the claimed issue carries %d label row(s), want the 1 it was seeded with — "+
			"the result omits labels, the claim must not DELETE them", got)
	}
}

// RunClaimerTakesAnOpenIssueTheActorAlreadyHolds pins the middle arm of the
// holder predicate in issueops/claimer.go's Claim doc — the issue is claimable
// while it is "unassigned, assigned to Actor, or assigned to a configured claim
// pool" — on the one shape that tells that arm apart from the idempotent
// re-claim beside it.
//
// A dispatcher assigns work without claiming it, so an OPEN issue carrying the
// actor's own name is an ordinary state, not an error. Claiming it is a real
// mutation: the status moves open -> in-progress, so ClaimResult.Changed is
// TRUE. The idempotent clause is narrower than this one — it requires
// StatusInProgress AND the same Actor — and an implementation that keyed
// idempotence on the assignee alone reports Changed false here and never writes
// the status. That is the whole reason this is its own case rather than a
// variant seeded inside the winning-path one.
func RunClaimerTakesAnOpenIssueTheActorAlreadyHolds(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-clmine"
	actor := fixture.IssuePrefix + "-holder"
	issue := claimerIssue(id, types.StatusOpen)
	issue.Assignee = actor
	seedClaimerIssue(t, ctx, fixture, issue)
	// The seed has to land the assignee, or the case degenerates into a second
	// copy of the unassigned winning path.
	assertClaimerRowState(t, ctx, fixture, id, string(types.StatusOpen), actor)

	result, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: actor, IssueID: id})
	if err != nil {
		t.Fatalf("Claim of an OPEN issue already assigned to the claiming actor: %v", err)
	}
	if !result.Changed {
		t.Error("Changed = false for a claim that moved an OPEN issue to in-progress; " +
			"the idempotent clause needs StatusInProgress as well as the same Actor")
	}
	if result.Issue == nil {
		t.Fatal("Claim returned a nil Issue on the winning path")
	}
	if result.Issue.Status != types.StatusInProgress || result.Issue.Assignee != actor {
		t.Errorf("returned row = (status %q, assignee %q), want (%q, %q)",
			result.Issue.Status, result.Issue.Assignee, types.StatusInProgress, actor)
	}
	assertClaimerRowState(t, ctx, fixture, id, string(types.StatusInProgress), actor)
}

// RunClaimerReclaimsItsOwnInProgressIssueWithoutWriting pins the idempotent
// clause of issueops/claimer.go's Claim doc — "StatusInProgress held by the
// same Actor is an idempotent success with Changed false and no persisted
// mutation" — and ClaimResult.Changed's own statement of what that false MEANS:
// "nothing was written and nothing was committed".
//
// All three halves are asserted, because each fails on its own:
//
//   - the CALL succeeds. A polling agent that re-claims after a transient
//     failure must not have to classify an error to learn it still holds the
//     issue.
//   - Changed is FALSE, and the returned row still reports the actor's claim.
//   - nothing was WRITTEN. updated_at is read as a raw row before and after: an
//     implementation that widened the claimable status set to include
//     in_progress would re-run the compare-and-set, stamp a new updated_at and
//     answer identically at every other assertion here. And nothing was
//     COMMITTED: the history length is unchanged, which is the half a polling
//     caller feels — one empty storage commit per poll.
func RunClaimerReclaimsItsOwnInProgressIssueWithoutWriting(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-clidem"
	actor := fixture.IssuePrefix + "-poller"
	seedClaimerIssue(t, ctx, fixture, claimerIssue(id, types.StatusOpen))

	first, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: actor, IssueID: id})
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if !first.Changed {
		t.Fatal("the first Claim reported no mutation; the re-claim below would then be testing nothing")
	}

	before := claimerHistoryCount(t, ctx, fixture)
	updatedBefore := claimerUpdatedAt(t, ctx, fixture, id)
	claimEventsBefore := claimerClaimEventCount(t, ctx, fixture, id)

	second, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: actor, IssueID: id})
	if err != nil {
		t.Fatalf("re-Claim by the actor that already holds the issue in progress: error = %v, want nil — "+
			"an agent retrying after a transient failure must not have to classify an error", err)
	}
	if second.Changed {
		t.Error("Changed = true for a re-claim that had nothing to change")
	}
	if second.Issue == nil {
		t.Fatal("the idempotent re-claim returned a nil Issue")
	}
	if second.Issue.Status != types.StatusInProgress || second.Issue.Assignee != actor {
		t.Errorf("returned row = (status %q, assignee %q), want the claim the actor already held (%q, %q)",
			second.Issue.Status, second.Issue.Assignee, types.StatusInProgress, actor)
	}

	if got := claimerUpdatedAt(t, ctx, fixture, id); got != updatedBefore {
		t.Errorf("updated_at moved %q -> %q across a re-claim that reported Changed false; "+
			"the compare-and-set ran anyway", updatedBefore, got)
	}
	if second.Issue.ID != id {
		t.Errorf("returned Issue.ID = %q, want %q", second.Issue.ID, id)
	}
	// THE LOAD-BEARING ASSERTION. The two checks above it both tie under a
	// compare-and-set that re-runs on a row the actor already holds; this one
	// does not, because the re-run records a second claim event whether or not
	// anything was committed.
	if got := claimerClaimEventCount(t, ctx, fixture, id); got != claimEventsBefore {
		t.Errorf("claim events went %d -> %d across a re-claim that reported Changed false; "+
			"the compare-and-set ran again", claimEventsBefore, got)
	}
	assertClaimerRowState(t, ctx, fixture, id, string(types.StatusInProgress), actor)
	assertClaimerCommittedNothing(t, ctx, fixture, before, "an idempotent re-claim")
}

// RunClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne pins the
// CONFIGURED half of the status arm in issueops/claimer.go's Claim doc: an
// issue is claimable "while the issue's status is built-in StatusOpen or a
// configured active status".
//
// The workspace installs two custom statuses, one in each category that
// matters, and the case asserts both directions:
//
//   - the ACTIVE-category custom status is claimable. Nothing else in this
//     contract can see that term; an implementation that hardcoded
//     `status = 'open'` — which is what both bodies said before bd-pq7m2 — is
//     green over every other case here.
//   - the WIP-category custom status is NOT. That is the decoy, and without it
//     the case above only says "some custom status was claimable", which an
//     implementation admitting EVERY configured status would also satisfy while
//     handing an agent an issue another agent is mid-way through.
//
// The refusal half also carries the refusal's shape — a wrapped ErrNotClaimable
// and a *ClaimConflictError naming the status that refused — and the state
// clause: a refused claim leaves the row exactly as it was and commits nothing.
func RunClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	active := fixture.IssuePrefix + "readyish"
	wip := fixture.IssuePrefix + "reviewing"
	if err := fixture.SetConfig(ctx, "status.custom", active+":active,"+wip+":wip"); err != nil {
		t.Fatalf("configure custom statuses: %v", err)
	}

	claimable := fixture.IssuePrefix + "-clcustom-active"
	refused := fixture.IssuePrefix + "-clcustom-wip"
	seedClaimerIssue(t, ctx, fixture, claimerIssue(claimable, types.Status(active)))
	seedClaimerIssue(t, ctx, fixture, claimerIssue(refused, types.Status(wip)))
	// Both seeds have to LAND in their custom statuses; a create that silently
	// normalized either to open would make one half vacuous and the other a
	// second copy of the closed-status case.
	assertClaimerRowState(t, ctx, fixture, claimable, active, "")
	assertClaimerRowState(t, ctx, fixture, refused, wip, "")

	result, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: claimable})
	if err != nil {
		t.Fatalf("Claim of an issue in the configured ACTIVE-category status %q: error = %v, want a win", active, err)
	}
	if !result.Changed {
		t.Error("Changed = false for a claim out of a configured active status")
	}
	assertClaimerRowState(t, ctx, fixture, claimable, string(types.StatusInProgress), claimerStranger)

	before := claimerHistoryCount(t, ctx, fixture)
	_, err = fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: refused})
	assertClaimerRefusal(t, err, publicops.ErrNotClaimable, refused, "", types.Status(wip),
		"a configured WIP-category status")
	assertClaimerRowState(t, ctx, fixture, refused, wip, "")
	assertClaimerCommittedNothing(t, ctx, fixture, before, "a claim refused by an ineligible status")
}

// RunClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder pins the two
// remaining arms of the holder predicate in issueops/claimer.go's Claim doc
// against each other: an issue "assigned to a configured claim pool" is
// claimable by anyone, and "a foreign holder refuses with a wrapped
// ErrAlreadyClaimed".
//
// They are one case because each is the other's control. A pool alias is, at
// the row level, indistinguishable from a foreign holder — the ONLY thing that
// separates the two issues seeded here is the claim.pools config — so a case
// that asserted the pool half alone would be satisfied by an implementation
// that had stopped protecting foreign claims altogether, and one that asserted
// the refusal alone would be satisfied by an implementation that had never
// learned about pools.
//
// THE POOL ALIAS IS NOT THE CLAIMING ACTOR. That is load-bearing: an alias
// equal to the actor would be admitted by the assigned-to-Actor arm and the
// case would pass with claim.pools ignored entirely.
//
// BOTH FOREIGN SHAPES ARE SEEDED, because the two bodies branch on them
// separately. An issue held in progress by another actor is the obvious one; an
// OPEN issue a dispatcher pre-assigned to another actor is the one whose
// refusal copy is composed by a different arm — it deliberately omits the
// parseable " by <assignee>" tail so callers recover the holder from the typed
// field — and it is the shape that used to fall through as claimable.
func RunClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	pool := fixture.IssuePrefix + "-crew"
	rival := fixture.IssuePrefix + "-rival"
	if err := fixture.SetConfig(ctx, "claim.pools", pool); err != nil {
		t.Fatalf("configure claim pools: %v", err)
	}

	pooled := fixture.IssuePrefix + "-clpool"
	openHeld := fixture.IssuePrefix + "-clforeign-open"
	inProgressHeld := fixture.IssuePrefix + "-clforeign-wip"
	for id, assignee := range map[string]string{pooled: pool, openHeld: rival, inProgressHeld: rival} {
		issue := claimerIssue(id, types.StatusOpen)
		issue.Assignee = assignee
		seedClaimerIssue(t, ctx, fixture, issue)
		assertClaimerRowState(t, ctx, fixture, id, string(types.StatusOpen), assignee)
	}
	// Move the third one into the rival's live claim through the role itself,
	// which is the only shape of in-progress this seam can produce.
	if _, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: rival, IssueID: inProgressHeld}); err != nil {
		t.Fatalf("seed the rival's live claim: %v", err)
	}

	// The pool alias is a stranger's to take.
	result, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: pooled})
	if err != nil {
		t.Fatalf("Claim of an issue assigned to the configured pool %q by an unrelated actor: error = %v, want a win", pool, err)
	}
	if !result.Changed {
		t.Error("Changed = false for a claim out of a configured pool")
	}
	assertClaimerRowState(t, ctx, fixture, pooled, string(types.StatusInProgress), claimerStranger)

	// A real holder's is not, in either of the two shapes.
	before := claimerHistoryCount(t, ctx, fixture)
	_, err = fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: openHeld})
	assertClaimerRefusal(t, err, publicops.ErrAlreadyClaimed, openHeld, rival, types.StatusOpen,
		"an OPEN issue a dispatcher pre-assigned to another actor")
	assertClaimerRowState(t, ctx, fixture, openHeld, string(types.StatusOpen), rival)

	_, err = fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: inProgressHeld})
	assertClaimerRefusal(t, err, publicops.ErrAlreadyClaimed, inProgressHeld, rival, types.StatusInProgress,
		"an issue another actor holds in progress")
	assertClaimerRowState(t, ctx, fixture, inProgressHeld, string(types.StatusInProgress), rival)
	assertClaimerCommittedNothing(t, ctx, fixture, before, "two claims refused by a foreign holder")
}

// RunClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt pins the
// BUILT-IN half of "an ineligible status [refuses] with a wrapped
// ErrNotClaimable" in issueops/claimer.go's Claim doc, and the
// *ClaimConflictError that comes with it.
//
// Two statuses are seeded, not one, because the two bodies compose the refusal
// out of the status AND the holder and only an UNASSIGNED row exercises the
// status arm cleanly:
//
//   - CLOSED and unassigned. The everyday refusal.
//   - IN PROGRESS and unassigned. A row nothing on this seam can produce — a
//     claim always leaves an assignee — so it is seeded directly. It is the
//     shape that says the anti-steal rule is about the STATUS: in_progress is
//     outside the claimable set even when the row names no holder to protect.
//
// ClaimConflictError.Assignee is asserted EMPTY on both, which is what the type
// says it means ("empty when the refusal was about the status rather than a
// foreign holder"); an implementation reporting the claiming actor there, or
// the previous holder, would have a caller announcing a conflict with nobody.
func RunClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	closed := fixture.IssuePrefix + "-clclosed"
	inProgress := fixture.IssuePrefix + "-clwip"
	seedClaimerIssue(t, ctx, fixture, claimerIssue(closed, types.StatusClosed))
	seedClaimerIssue(t, ctx, fixture, claimerIssue(inProgress, types.StatusInProgress))
	assertClaimerRowState(t, ctx, fixture, closed, string(types.StatusClosed), "")
	assertClaimerRowState(t, ctx, fixture, inProgress, string(types.StatusInProgress), "")

	before := claimerHistoryCount(t, ctx, fixture)
	for _, refusal := range []struct {
		id      string
		status  types.Status
		subject string
	}{
		{closed, types.StatusClosed, "a closed issue"},
		{inProgress, types.StatusInProgress, "an UNASSIGNED in-progress issue"},
	} {
		_, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: refusal.id})
		assertClaimerRefusal(t, err, publicops.ErrNotClaimable, refusal.id, "", refusal.status, refusal.subject)
		assertClaimerRowState(t, ctx, fixture, refusal.id, string(refusal.status), "")
	}
	assertClaimerCommittedNothing(t, ctx, fixture, before, "two claims refused by an ineligible status")
}

// RunClaimerRefusesAWispIDAsNotFound pins the plane clause of
// issueops/claimer.go's Claim doc: "A wisp id is ErrNotFound: the wisp plane is
// not claimable through this role."
//
// ErrNotFound rather than ErrNotClaimable is the whole point. The wisp is
// seeded READY AND CLAIMABLE by every other measure — open, unassigned, no
// blockers — so the refusal cannot be about its state; it is about which plane
// the role addresses. The CAS underneath is perfectly capable of claiming a
// wisp (the store's own ClaimIssue wrapper routes one there and succeeds, which
// is why that route has its own residue test), so this refusal is a decision
// the role makes and not a limitation it inherits.
//
// The state half is asserted on BOTH planes, because a body that refused after
// claiming and a body that refused after promoting both return the same error:
// the wisps row must still be open and unassigned, and no durable row may have
// appeared under that id.
func RunClaimerRefusesAWispIDAsNotFound(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	id := fixture.IssuePrefix + "-clwisp"
	seedClaimerWisp(t, ctx, fixture, claimerIssue(id, types.StatusOpen))
	assertClaimerPlaneRowState(t, ctx, fixture, "wisps", id, string(types.StatusOpen), "")

	before := claimerHistoryCount(t, ctx, fixture)
	_, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: claimerStranger, IssueID: id})
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("Claim of a ready, unassigned WISP: error = %v, want ErrNotFound — the wisp plane is not claimable through this role", err)
	}
	var conflict *publicops.ClaimConflictError
	if errors.As(err, &conflict) {
		t.Errorf("Claim of a wisp reported a *ClaimConflictError (%#v); a wisp id is not a conflict, it is absent from this plane", conflict)
	}

	assertClaimerPlaneRowState(t, ctx, fixture, "wisps", id, string(types.StatusOpen), "")
	assertClaimerRowAbsent(t, ctx, fixture, "issues", id)
	assertClaimerCommittedNothing(t, ctx, fixture, before, "a claim refused because the id names a wisp")
}

// RunClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState pins
// "Refusals and deterministic validation failures leave persistent state
// unchanged" from issueops/claimer.go's Claim doc, on the three refusals that
// never reach a row at all.
//
// The two empty-field refusals are not idle. An empty Actor is the dangerous
// one: the compare-and-set admits an unassigned row, and `assignee = ”` is
// what an unassigned row holds, so an implementation that dropped the check
// does not fail — it CLAIMS the issue for nobody and reports success. The
// claimable decoy seeded here is what makes that visible; it is checked
// afterwards as a raw row, still open and still unassigned.
//
// SPEC GAP. The leaf says these failures are "deterministic validation
// failures" and that they leave state alone, but it never names the sentinel
// they carry — every other role in this family says "match ErrValidation" in so
// many words. All three implementations answer ErrValidation, so the assertion
// records an observed three-way agreement that the doc has not yet ratified.
//
// The absent id is here for the same reason a wisp id is ErrNotFound: an id
// that names nothing is not a conflict, and a caller that had to read the prose
// to tell "someone else has it" from "it does not exist" would be parsing
// messages.
func RunClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	decoy := fixture.IssuePrefix + "-clreject"
	seedClaimerIssue(t, ctx, fixture, claimerIssue(decoy, types.StatusOpen))

	before := claimerHistoryCount(t, ctx, fixture)
	for _, refusal := range []struct {
		name    string
		request publicops.ClaimRequest
		want    error
	}{
		{"an empty actor", publicops.ClaimRequest{IssueID: decoy}, publicops.ErrValidation},
		{"an empty issue id", publicops.ClaimRequest{Actor: claimerStranger}, publicops.ErrValidation},
		{"an id that names nothing", publicops.ClaimRequest{Actor: claimerStranger, IssueID: decoy + "-absent"}, publicops.ErrNotFound},
	} {
		if _, err := fixture.Claimer.Claim(ctx, refusal.request); !errors.Is(err, refusal.want) {
			t.Errorf("Claim with %s: error = %v, want %v", refusal.name, err, refusal.want)
		}
	}

	// The row a valid request would have won is still there, untouched.
	assertClaimerRowState(t, ctx, fixture, decoy, string(types.StatusOpen), "")
	assertClaimerCommittedNothing(t, ctx, fixture, before, "three refused requests")
}

// claimerStranger is the actor most cases claim as: a name that appears nowhere
// in any fixture, as creator or assignee. Claiming as a stranger is how the
// "any actor may claim" clause is asserted without a case of its own —
// eligibility is decided by the issue's state alone, so an actor with no prior
// relationship to the issue wins exactly what any other actor would.
const claimerStranger = "a-stranger"

func claimerIssue(id string, status types.Status) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    status,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

func seedClaimerIssue(t *testing.T, ctx context.Context, fixture ClaimerFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func seedClaimerWisp(t *testing.T, ctx context.Context, fixture ClaimerFixture, issue *types.Issue) {
	t.Helper()
	issue.Ephemeral = true
	if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", issue.ID, err)
	}
}

// assertClaimerRefusal checks the whole shape of one refusal: the sentinel it
// matches, that it is a *ClaimConflictError, and that the conflict carries the
// state the losing transaction read.
//
// The sentinel and the typed conflict are checked separately rather than as one
// errors.As, because they fail for different reasons — a body that stopped
// wrapping still matches the sentinel, and a body that returned the wrong
// sentinel still produces a conflict — and a caller depends on both: errors.Is
// routes the exit code, the typed fields name the holder.
func assertClaimerRefusal(t *testing.T, err error, sentinel error, id, wantAssignee string, wantStatus types.Status, subject string) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Errorf("Claim of %s: error = %v, want it to match %v", subject, err, sentinel)
		return
	}
	var conflict *publicops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Errorf("Claim of %s: error = %v, want a *ClaimConflictError carrying the state that refused it", subject, err)
		return
	}
	if conflict.IssueID != id || conflict.Assignee != wantAssignee || conflict.Status != wantStatus {
		t.Errorf("Claim of %s: conflict = (id %q, assignee %q, status %q), want (%q, %q, %q)",
			subject, conflict.IssueID, conflict.Assignee, conflict.Status, id, wantAssignee, wantStatus)
	}
}

// assertClaimerRowState reads the durable row's status and assignee back out of
// the database. The assignee comes through COALESCE because an unclaimed row's
// column may be NULL or empty depending on how it was seeded, and the three
// fixtures' scan paths do not agree on what a NULL means.
func assertClaimerRowState(t *testing.T, ctx context.Context, fixture ClaimerFixture, id, wantStatus, wantAssignee string) {
	t.Helper()
	assertClaimerPlaneRowState(t, ctx, fixture, "issues", id, wantStatus, wantAssignee)
}

func assertClaimerPlaneRowState(t *testing.T, ctx context.Context, fixture ClaimerFixture, table, id, wantStatus, wantAssignee string) {
	t.Helper()
	var status, assignee string
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT status, COALESCE(assignee, '') FROM " + table + " WHERE id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{id}, &status, &assignee); err != nil {
		t.Fatalf("read %s row %s: %v", table, id, err)
	}
	if status != wantStatus || assignee != wantAssignee {
		t.Errorf("%s row %s = (status %q, assignee %q), want (%q, %q)",
			table, id, status, assignee, wantStatus, wantAssignee)
	}
}

// assertClaimerRowAbsent checks that a plane holds no row for id. It counts
// rather than scanning one, so "absent" and "present but unreadable" cannot be
// confused: a failed scan would be a fixture error, an empty count is the
// answer.
func assertClaimerRowAbsent(t *testing.T, ctx context.Context, fixture ClaimerFixture, table, id string) {
	t.Helper()
	var rows int
	//nolint:gosec // G201: table is one of the contract's two hardcoded names.
	query := "SELECT COUNT(*) FROM " + table + " WHERE id = ?"
	if err := fixture.QueryScalar(ctx, query, []any{id}, &rows); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	if rows != 0 {
		t.Errorf("%s holds %d row(s) for %s, want 0", table, rows, id)
	}
}

// claimerLabelCount reports how many label rows the durable plane holds for id.
// It is what keeps the bare-row assertion honest at both ends: the result must
// omit labels the issue REALLY HAS, and the claim must not delete them.
func claimerLabelCount(t *testing.T, ctx context.Context, fixture ClaimerFixture, id string) int {
	t.Helper()
	var rows int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", []any{id}, &rows); err != nil {
		t.Fatalf("count labels for %s: %v", id, err)
	}
	return rows
}

// claimerUpdatedAt reads one row's updated_at as text. It is compared as an
// opaque string: the question is only whether a write touched it, and the three
// fixtures' scan paths render timestamps differently.
func claimerUpdatedAt(t *testing.T, ctx context.Context, fixture ClaimerFixture, id string) string {
	t.Helper()
	var stamp string
	if err := fixture.QueryScalar(ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{id}, &stamp); err != nil {
		t.Fatalf("read updated_at for %s: %v", id, err)
	}
	return stamp
}

// claimerClaimEventCount counts the CLAIM EVENTS on one issue.
//
// It exists because the two detectors beside it are provably blind on the store
// legs, which a review demonstrated with a surviving mutant: widen the
// compare-and-set so a re-claim rewrites the row, and `updated_at` still ties
// (the column is DATETIME with no fractional precision, so both claims land in
// the same second) while the history delta stays 0 (`Changed` is computed from
// the pre-image, so the commit is skipped and the event row is written but
// never committed). The event row is the one thing that moves.
//
// The deleted audit-suite Claimer contract counted exactly this, and it was the
// only deterministic role-seam detector for "an idempotent re-claim persists
// nothing". Losing it in the consolidation was a real regression; this restores
// it through the QueryScalar hook the fixture already carries.
func claimerClaimEventCount(t *testing.T, ctx context.Context, fixture ClaimerFixture, id string) int {
	t.Helper()
	var events int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		[]any{id, string(types.EventClaimed)}, &events); err != nil {
		t.Fatalf("count claim events for %s: %v", id, err)
	}
	return events
}

func claimerHistoryCount(t *testing.T, ctx context.Context, fixture ClaimerFixture) int {
	t.Helper()
	if fixture.CountHistory == nil {
		return -1
	}
	entries, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history: %v", err)
	}
	return entries
}

// assertClaimerCommittedNothing checks that the durable history did not move.
// Zero is the only delta this contract asserts, and that is a statement about
// the leaf rather than a shortcut: every history clause it makes is a "nothing
// was committed" one — ClaimResult.Changed's, about the idempotent re-claim,
// and Claim's, about a refusal. The winning path's commit is deliberately not
// asserted; the leaf promises a persisted MUTATION there and says nothing about
// version control, so a count would be pinning the backends instead.
//
// A fixture that cannot observe history reports -1 from claimerHistoryCount and
// the check is skipped rather than passing on an arithmetic accident.
func assertClaimerCommittedNothing(t *testing.T, ctx context.Context, fixture ClaimerFixture, before int, subject string) {
	t.Helper()
	if before < 0 {
		return
	}
	if after := claimerHistoryCount(t, ctx, fixture); after != before {
		t.Errorf("history entries went %d -> %d across %s, want no change", before, after, subject)
	}
}

// RunClaimerRefusalMessagesCarryTheirFragments pins the PROSE of the two
// refusals, and it is here because a caller that cannot classify typed errors
// still reads them.
//
// It came from the audit-suite copy of this contract (removed in the same
// commit that added this case), which ran on the store legs only: the audit
// Factory is `func(*testing.T) storage.DoltStorage`, and a unit-of-work
// provider is not one, so the uow body's message shapes were never compared to
// the stores'. They are now.
//
// The last arm is the one worth keeping: the open-but-assigned refusal OMITS
// the "claimed by" fragment on purpose, while the typed conflict still carries
// the holder. A caller reading prose learns less there than a caller reading
// the struct, and that asymmetry is deliberate rather than a message bug.
func RunClaimerRefusalMessagesCarryTheirFragments(t *testing.T, ctx context.Context, fixture ClaimerFixture) {
	t.Helper()
	held := fixture.IssuePrefix + "-frag-held"
	closed := fixture.IssuePrefix + "-frag-closed"
	assigned := fixture.IssuePrefix + "-frag-assigned"

	seedClaimerIssue(t, ctx, fixture, claimerIssue(held, types.StatusOpen))
	seedClaimerIssue(t, ctx, fixture, claimerIssue(closed, types.StatusClosed))
	assignedIssue := claimerIssue(assigned, types.StatusOpen)
	assignedIssue.Assignee = "alice"
	seedClaimerIssue(t, ctx, fixture, assignedIssue)

	if _, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: "alice", IssueID: held}); err != nil {
		t.Fatalf("first claim of %s: %v", held, err)
	}

	_, err := fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: "bob", IssueID: held})
	conflict := claimerConflict(t, err, publicops.ErrAlreadyClaimed, held)
	if got := claimerRecoveredTail(err.Error(), publicops.ErrAlreadyClaimed, storage.ClaimedByFragment); got != "alice" {
		t.Errorf("assignee recovered from the in_progress conflict = %q, want alice (message %q)", got, err.Error())
	}
	if conflict.Assignee != "alice" {
		t.Errorf("conflict.Assignee = %q, want alice", conflict.Assignee)
	}

	_, err = fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: "carol", IssueID: closed})
	conflict = claimerConflict(t, err, publicops.ErrNotClaimable, closed)
	if got := claimerRecoveredTail(err.Error(), publicops.ErrNotClaimable, storage.NotClaimableStatusFragment); got != string(types.StatusClosed) {
		t.Errorf("status recovered from the not-claimable refusal = %q, want %q (message %q)", got, types.StatusClosed, err.Error())
	}
	if conflict.Status != types.StatusClosed {
		t.Errorf("conflict.Status = %q, want %q", conflict.Status, types.StatusClosed)
	}

	_, err = fixture.Claimer.Claim(ctx, publicops.ClaimRequest{Actor: "bob", IssueID: assigned})
	conflict = claimerConflict(t, err, publicops.ErrAlreadyClaimed, assigned)
	if got := claimerRecoveredTail(err.Error(), publicops.ErrAlreadyClaimed, storage.ClaimedByFragment); got != "" {
		t.Errorf("the open-but-assigned refusal carried the %q fragment (recovered %q from %q); that copy omits it on purpose",
			storage.ClaimedByFragment, got, err.Error())
	}
	if conflict.Assignee != "alice" {
		t.Errorf("conflict.Assignee = %q, want alice — the holder the prose does not name must still reach the caller typed", conflict.Assignee)
	}
}

// claimerConflict asserts the refusal wraps sentinel AND arrives as a typed
// *ClaimConflictError naming the issue, then hands the conflict back.
func claimerConflict(t *testing.T, err error, sentinel error, wantID string) *publicops.ClaimConflictError {
	t.Helper()
	if err == nil {
		t.Fatalf("claim of %s succeeded, want a refusal wrapping %v", wantID, sentinel)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("refusal for %s = %v, want one wrapping %v", wantID, err, sentinel)
	}
	var conflict *publicops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("refusal for %s is not a *issueops.ClaimConflictError (%v); a caller cannot classify the conflict without parsing prose", wantID, err)
	}
	if conflict.IssueID != wantID {
		t.Errorf("conflict.IssueID = %q, want %q", conflict.IssueID, wantID)
	}
	return conflict
}

// claimerRecoveredTail returns what a message carries after `sentinel+fragment`,
// or "" when the fragment is absent. It reads the tail rather than matching the
// whole string so a case pins WHAT the prose names without pinning how the rest
// of the sentence is worded.
func claimerRecoveredTail(msg string, sentinel error, fragment string) string {
	marker := sentinel.Error() + fragment
	i := strings.LastIndex(msg, marker)
	if i < 0 {
		return ""
	}
	tail := msg[i+len(marker):]
	if strings.ContainsAny(tail, " \t\r\n(") {
		return ""
	}
	return tail
}
