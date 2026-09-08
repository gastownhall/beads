package conformance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.ReadyClaimer must satisfy. There are three accessors answering the
// role — the server-backed store, the embedded store, and the unit-of-work
// provider — but only two BODIES: the first two share
// internal/storage/issueops's ValidateClaimNextRequest and ExecuteClaimNext and
// differ in the transaction wrapper and the engine beneath it, while the
// unit-of-work
// implementation is separate code that reuses the same validator and the same
// workapi.BuildReadyFilter. Wiring all three is still worth it — the third run
// is what catches wrapper and engine divergence, which is where the claim's
// commit message diverged before the role existed — but a case passing three
// times is not three independent votes on the body.
//
// EVERY CASE IS SCOPED BY A LABEL, and that is load-bearing rather than
// stylistic. The ready front is a property of the whole database, not of an id
// prefix, so a claim seeded by one assertion is claimable by the next one. Each
// case therefore seeds rows carrying a label only it uses and asks its question
// through Filter.Labels, which is what lets the whole suite share one database
// — the unit-of-work wiring's one-provider-many-subtests shape depends on it,
// because a fresh provider there boots a real Dolt sql-server.
//
// EVERY CASE NAMES A SORT POLICY. ReadyRequest.Sort's empty value resolves to
// hybrid at the storage layer, which the leaf says no front door may rely on
// (issueops/reader.go:79-83); a contract that relied on it would be pinning the
// fallback instead of the order.
//
// NOT IN THIS CONTRACT: two racing claimers producing exactly one winner. That
// is a transaction property of the persistence boundary, not a promised result
// of the operation, and it belongs per-backend if it is written at all.

// ReadyClaimerFixture supplies adapter-specific storage access for the
// ready-claim assertions.
type ReadyClaimerFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	Claimer     publicops.ReadyClaimer
	// Reader is the SAME backend's reader accessor. One case needs it: the
	// claim's filter is Reader.Ready's filter in the leaf's words ("the same
	// type, not a parallel one shaped like it"), and the only way to observe
	// that end to end is to ask both surfaces the one question and compare.
	Reader publicops.Reader
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge. The claim hydrates the row it wins, so a
	// case that wants to see a non-zero cardinality has to put one there.
	AddDependency func(context.Context, *types.Dependency, string) error
	QueryScalar   func(context.Context, string, []any, ...any) error
	// CountHistory reports the fixture's durable history length, for the "at
	// most one entry per call, none when nothing was eligible" clause. Nil
	// means the backend cannot observe history: the case whose whole subject is
	// that clause then SKIPS with that reason rather than passing quietly, and
	// the cases that merely carry a delta check alongside their own subject
	// drop the check and keep their subject. It is non-nil on all three
	// fixtures today.
	CountHistory func(context.Context) (int, error)
}

// RunReadyClaimerRejectsLimitOffsetBriefAndEmptyActor pins the deterministic
// request-validation refusals — Limit set, Offset set, Brief set, Actor empty
// (issueops/readyclaimer.go:15-23 and :7-8) — and the state clause that comes
// with them: "a validation failure leaves persistent state unchanged"
// (issueops/readyclaimer.go:56).
//
// Rejecting Limit and Offset is the visible half of a rule about the SCAN. A
// claim delivers one row however large the pool it walked, and the walk has to
// stay unbounded so a window that happens to be unclaimable does not report
// "nothing to claim" while other ready work waits. Accepting the two fields and
// quietly dropping them would be the same behavior with the promise hidden.
// An explicit zero limit is refused with the rest: Limit is a pointer so that
// "unset" and "explicitly unlimited" stay distinguishable
// (issueops/reader.go:85-100), and only the first of those is what this request
// permits.
//
// Brief is refused on a sharper version of the same argument. The claim does
// not read its row through the page's query at all — it refetches the winner
// whole and hydrates the cardinalities itself — so the projection has nothing
// to apply to. Accepting it would return a fully-hydrated row, carrying
// IsLitePartial=false, from a request that has ALREADY MUTATED STATE: the one
// field a caller could check to notice would be the field asserting nothing
// went wrong. That is why it is here and not merely documented.
//
// The claimable row seeded here is the point of the state half: each refusal
// runs against a front that WOULD have yielded it, so an implementation that
// validated after claiming fails on the row rather than on the error.
//
// WHAT IS DELIBERATELY NOT ASSERTED: the result value. The leaf says "Result
// values are unspecified when error is non-nil" (issueops/readyclaimer.go:55),
// so a case checking that the refused call also returned a zero result would be
// pinning today's implementations rather than the contract. The state
// assertions below are the part the contract does promise.
func RunReadyClaimerRejectsLimitOffsetBriefAndEmptyActor(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcreject"
	candidate := fixture.IssuePrefix + "-rcreject-a"
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(candidate, 1, label))

	before := readyClaimerHistoryCount(t, ctx, fixture)
	limit := 5
	unlimited := 0
	for _, refusal := range []struct {
		name    string
		request publicops.ClaimNextRequest
	}{
		{"limit", publicops.ClaimNextRequest{
			Actor:  "claimer",
			Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort, Limit: &limit},
		}},
		{"explicitly unlimited limit", publicops.ClaimNextRequest{
			Actor:  "claimer",
			Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort, Limit: &unlimited},
		}},
		{"offset", publicops.ClaimNextRequest{
			Actor:  "claimer",
			Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort, Offset: 1},
		}},
		{"brief", publicops.ClaimNextRequest{
			Actor:  "claimer",
			Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort, Brief: true},
		}},
		{"empty actor", publicops.ClaimNextRequest{
			Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort},
		}},
	} {
		if _, err := fixture.Claimer.ClaimNext(ctx, refusal.request); !errors.Is(err, storage.ErrValidation) {
			t.Errorf("ClaimNext with %s: error = %v, want ErrValidation", refusal.name, err)
		}
	}

	// Unchanged means unchanged: the row a valid request would have won is
	// still open and unassigned, and nothing was written to history.
	assertReadyClaimerRowState(t, ctx, fixture, candidate, string(types.StatusOpen), "")
	assertReadyClaimerHistoryDelta(t, ctx, fixture, before, 0, "five refused requests")
}

// RunReadyClaimerEmptyFrontIsNormal pins the outcome most likely to diverge: a
// drained queue answers with a nil Claimed and a NIL ERROR
// (issueops/readyclaimer.go:32-37). An empty front is the steady state of a
// polling agent, and one that had to classify an error to discover it would be
// pattern-matching prose. Nothing is written and no history entry is recorded
// (issueops/readyclaimer.go:35-36, :71-72), so an implementation that commits an
// empty transaction fails here too.
//
// The decoy is what makes the case about the FRONT rather than about an empty
// database: a ready, claimable row exists throughout, it simply does not match
// the filter, and it must still be sitting there afterwards.
func RunReadyClaimerEmptyFrontIsNormal(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcempty"
	decoy := fixture.IssuePrefix + "-rcempty-decoy"
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(decoy, 0, label+"-other"))

	before := readyClaimerHistoryCount(t, ctx, fixture)
	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{
		Actor:  "claimer",
		Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort},
	})
	if err != nil {
		t.Fatalf("ClaimNext against an empty ready front: error = %v, want nil — an empty front is a normal outcome, not an error", err)
	}
	if result.Claimed != nil {
		t.Fatalf("ClaimNext against an empty ready front claimed %s, want nil", result.Claimed.ID)
	}

	assertReadyClaimerRowState(t, ctx, fixture, decoy, string(types.StatusOpen), "")
	assertReadyClaimerHistoryDelta(t, ctx, fixture, before, 0, "a claim that found nothing")
}

// RunReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState pins the winning
// path: the first row in the requested order moves to in-progress with the
// assignee set to Actor, and the RETURNED row is the post-claim row rather than
// the pre-claim one (issueops/readyclaimer.go:28-31).
//
// What it does NOT try to prove is the transactional simultaneity itself. That
// selection, the compare-and-set and the hydration share one transaction is not
// black-box observable; what is observable is that the snapshot handed back
// equals the state left in the database, and that the cardinalities are
// populated from real edges rather than zeroed. Both halves of that are here,
// and the seeded edges are why the count assertion can be an equality instead of
// a "not obviously empty".
func RunReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcwin"
	winner := fixture.IssuePrefix + "-rcwin-a"
	runnerUp := fixture.IssuePrefix + "-rcwin-b"
	// A CLOSED blocker: it gives the winner a dependency to count without
	// taking it off the ready front.
	blocker := fixture.IssuePrefix + "-rcwin-blocker"
	dependent := fixture.IssuePrefix + "-rcwin-dependent"

	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(winner, 0, label))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(runnerUp, 1, label))
	closed := readyClaimerIssue(blocker, 1, label+"-blocker")
	closed.Status = types.StatusClosed
	seedReadyClaimerIssue(t, ctx, fixture, closed)
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(dependent, 1, label+"-dependent"))
	seedReadyClaimerEdge(t, ctx, fixture, winner, blocker)
	seedReadyClaimerEdge(t, ctx, fixture, dependent, winner)

	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{
		Actor:  "claimer",
		Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort},
	})
	if err != nil {
		t.Fatalf("ClaimNext against a seeded ready front: %v", err)
	}
	if result.Claimed == nil {
		t.Fatal("ClaimNext returned no row against a front holding two claimable issues")
	}
	if result.Claimed.ID != winner {
		t.Fatalf("claimed %s, want %s — the first row of the requested order", result.Claimed.ID, winner)
	}
	if result.Claimed.Issue == nil {
		t.Fatal("claimed row carries a nil Issue")
	}
	if result.Claimed.Issue.Status != types.StatusInProgress {
		t.Errorf("returned status = %q, want %q", result.Claimed.Issue.Status, types.StatusInProgress)
	}
	if result.Claimed.Issue.Assignee != "claimer" {
		t.Errorf("returned assignee = %q, want %q", result.Claimed.Issue.Assignee, "claimer")
	}
	if result.Claimed.DependencyCount != 1 {
		t.Errorf("returned DependencyCount = %d, want 1 — the claim hydrates the row it won", result.Claimed.DependencyCount)
	}
	if result.Claimed.DependentCount != 1 {
		t.Errorf("returned DependentCount = %d, want 1 — the claim hydrates the row it won", result.Claimed.DependentCount)
	}

	// The returned snapshot IS the stored state, not a projection of the row
	// as it was before the compare-and-set.
	assertReadyClaimerRowState(t, ctx, fixture,
		winner, string(result.Claimed.Issue.Status), result.Claimed.Issue.Assignee)
	assertReadyClaimerRowState(t, ctx, fixture, winner, string(types.StatusInProgress), "claimer")
	// One row is won per call, so the runner-up is untouched.
	assertReadyClaimerRowState(t, ctx, fixture, runnerUp, string(types.StatusOpen), "")
}

// RunReadyClaimerClaimsAnEphemeralRowTheFilterAdmits pins the ephemeral half of
// the ready set: "a filter that sets IncludeEphemeral pulls [ephemeral rows] in
// for the claim exactly as it does for the listing: such a row IS claimable"
// (issueops/readyclaimer.go:63-76). The wisp seeded here is ready by every other
// measure and the request opts ephemeral rows in, so the listing offers it — and
// the claim takes it.
//
// The case asserts through Reader.Ready first, and fails there rather than
// silently degenerating, because "the claim took the row the listing offered" is
// only meaningful when the listing offered one. A seed that never reached the
// front would make this a second copy of the winning-path case.
//
// Three things beyond "it returned a row" are asserted, because each is a
// separate way an implementation could plausibly interpret the clause and get it
// wrong:
//   - the EPHEMERAL row moved. The claim lands on the row that was offered, not
//     on a durable copy of it, so the wisps row carries the actor.
//   - no durable row appeared. Claiming an ephemeral row must not promote it.
//   - durable history did not move. Ephemeral rows are not versioned, so the
//     leaf's "a claim that wins an EPHEMERAL row records none either" is the
//     visible consequence a `bd dolt log` reader would otherwise be surprised
//     by. This is the ONE ready-claim outcome that mutates state without a
//     history entry, which is exactly why it is pinned rather than assumed.
//
// bd-yby99.4 adjudicated this. The case previously asserted the opposite — that
// the claim declines a ready wisp — on the strength of a leaf sentence reading
// "the ready set is the issues plane". All three backends claimed the wisp
// anyway; the owner ruled the implementations right and the sentence wrong, so
// the sentence and this case inverted together.
func RunReadyClaimerClaimsAnEphemeralRowTheFilterAdmits(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcwisp"
	wisp := fixture.IssuePrefix + "-rcwisp-w"
	seedReadyClaimerWisp(t, ctx, fixture, readyClaimerIssue(wisp, 0, label))

	request := publicops.ReadyRequest{
		Labels:           []string{label},
		Sort:             readyClaimerSort,
		IncludeEphemeral: true,
	}
	page, err := fixture.Reader.Ready(ctx, request)
	if err != nil {
		t.Fatalf("Reader.Ready over the seeded wisp: %v", err)
	}
	if !readyClaimerPageHas(page, wisp) {
		t.Fatalf("Reader.Ready did not offer the seeded ready wisp %s (page: %v); "+
			"the case cannot say the claim took a row the front never held", wisp, readyClaimerPageIDs(page))
	}

	before := readyClaimerHistoryCount(t, ctx, fixture)
	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{Actor: "claimer", Filter: request})
	if err != nil {
		t.Fatalf("ClaimNext over a front holding one ready wisp: error = %v, want nil", err)
	}
	if result.Claimed == nil {
		t.Fatal("ClaimNext returned no row against a front holding one ready wisp that IncludeEphemeral admitted")
	}
	if result.Claimed.ID != wisp {
		t.Fatalf("claimed %s, want the ephemeral row %s the filter admitted", result.Claimed.ID, wisp)
	}
	if result.Claimed.Issue == nil {
		t.Fatal("claimed row carries a nil Issue")
	}
	if result.Claimed.Issue.Status != types.StatusInProgress {
		t.Errorf("returned status = %q, want %q", result.Claimed.Issue.Status, types.StatusInProgress)
	}
	if result.Claimed.Issue.Assignee != "claimer" {
		t.Errorf("returned assignee = %q, want %q", result.Claimed.Issue.Assignee, "claimer")
	}

	assertReadyClaimerWispState(t, ctx, fixture, wisp, string(types.StatusInProgress), "claimer")
	assertReadyClaimerRowAbsent(t, ctx, fixture, "issues", wisp)
	assertReadyClaimerHistoryDelta(t, ctx, fixture, before, 0, "a claim that won an ephemeral row")
}

// RunReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet pins the DEFAULT
// half of the same clause the case above pins the opt-in half of: "Ephemeral
// rows are outside [the ready set] by default, and a filter that sets
// IncludeEphemeral pulls them in for the claim exactly as it does for the
// listing" (issueops/readyclaimer.go:65-67). A request that never mentions
// ephemeral rows never claims one.
//
// The two halves fail independently and only one of them was pinned anywhere.
// The reader contract pins the default gate for the LISTING
// (RunReaderReadyDeferredAndEphemeralGates), not for the claim; and after
// bd-yby99.4 inverted the opt-in case, an implementation that overcorrected
// into always admitting wisps would hand an agent work `bd ready` never offered
// it — the failure that clause exists to prevent — with nothing failing.
//
// The listing probe is what keeps the nil honest. The seeded wisp is ready by
// every other measure, and the ONLY difference between the request that offers
// it and the request that must not claim it is IncludeEphemeral, so a nil
// Claimed here cannot be a mis-seeded row or a label that matches nothing.
func RunReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcdefault"
	wisp := fixture.IssuePrefix + "-rcdefault-w"
	seedReadyClaimerWisp(t, ctx, fixture, readyClaimerIssue(wisp, 0, label))

	admitted := publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort, IncludeEphemeral: true}
	page, err := fixture.Reader.Ready(ctx, admitted)
	if err != nil {
		t.Fatalf("Reader.Ready over the seeded wisp with IncludeEphemeral: %v", err)
	}
	if !readyClaimerPageHas(page, wisp) {
		t.Fatalf("Reader.Ready did not offer the seeded ready wisp %s even with IncludeEphemeral (page: %v); "+
			"a nil claim below would then say nothing about the default gate", wisp, readyClaimerPageIDs(page))
	}

	before := readyClaimerHistoryCount(t, ctx, fixture)
	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{
		Actor:  "claimer",
		Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort},
	})
	if err != nil {
		t.Fatalf("ClaimNext with IncludeEphemeral unset: error = %v, want nil — a front holding only ephemeral rows is an EMPTY front, not a failure", err)
	}
	if result.Claimed != nil {
		t.Fatalf("ClaimNext claimed %s with IncludeEphemeral unset; ephemeral rows are outside the default ready set, "+
			"so this hands an agent work the listing never offered it", result.Claimed.ID)
	}

	assertReadyClaimerWispState(t, ctx, fixture, wisp, string(types.StatusOpen), "")
	assertReadyClaimerHistoryDelta(t, ctx, fixture, before, 0, "a default claim over a front holding one ephemeral row")
}

// RunReadyClaimerLeasesADurableWinButNotAnEphemeralOne pins both halves of the
// lease clause, in one case, because each half is what makes the other mean
// something.
//
// THE AFFIRMATIVE: "A DURABLE win grants EXACTLY ONE LEASE on the row it won"
// (issueops/readyclaimer.go:78-82). THE NEGATIVE: "An ephemeral win carries NO
// LEASE, which is the sharper of the two consequences because it has no expiry
// to wait out" (readyclaimer.go:84-90).
//
// Asserting the zero alone would be half vacuous: a backend whose leases table
// this fixture cannot reach reports zero for the wisp too, and a backend that
// stopped leasing durable claims entirely would pass. Asserting the one alone
// would say nothing about the recovery story the leaf tells a caller it does
// NOT get for a wisp. Together the pair says the query can see lease rows AND
// that the plane is what decides whether there is one.
//
// The clause's other half — heartbeats refuse an ephemeral row, lease-expiry
// recovery only walks leased durable ones — is a promise about OTHER verbs and
// is not observable through ClaimNext; the lease row is the part this seam can
// see, and it is what makes those two true.
//
// (This was a spec gap, bd-yby99.13: the durable check was a probe rather
// than an assertion; the owner ratified the affirmative and the probe became
// the assertion it already looked like.)
func RunReadyClaimerLeasesADurableWinButNotAnEphemeralOne(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	ephemeralLabel := fixture.IssuePrefix + "-rclease-eph"
	durableLabel := fixture.IssuePrefix + "-rclease-dur"
	wisp := fixture.IssuePrefix + "-rclease-w"
	durable := fixture.IssuePrefix + "-rclease-d"
	seedReadyClaimerWisp(t, ctx, fixture, readyClaimerIssue(wisp, 0, ephemeralLabel))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(durable, 0, durableLabel))

	won := readyClaimerWin(t, ctx, fixture, publicops.ReadyRequest{
		Labels: []string{ephemeralLabel}, Sort: readyClaimerSort, IncludeEphemeral: true,
	})
	if won != wisp {
		t.Fatalf("claimed %s, want the ephemeral row %s", won, wisp)
	}
	assertReadyClaimerWispState(t, ctx, fixture, wisp, string(types.StatusInProgress), "claimer")
	if leases := readyClaimerLeaseCount(t, ctx, fixture, wisp); leases != 0 {
		t.Errorf("the ephemeral win left %d leases row(s) for %s, want 0 — a leased wisp joins the lease-expiry "+
			"recovery walk, which is the recovery story the leaf tells a caller it does NOT get", leases, wisp)
	}

	won = readyClaimerWin(t, ctx, fixture, publicops.ReadyRequest{Labels: []string{durableLabel}, Sort: readyClaimerSort})
	if won != durable {
		t.Fatalf("claimed %s, want the durable row %s", won, durable)
	}
	if leases := readyClaimerLeaseCount(t, ctx, fixture, durable); leases != 1 {
		t.Fatalf("the durable win left %d leases row(s) for %s, want exactly 1 — a durable claim with no lease is work "+
			"nothing can take back, because heartbeats have no handle to extend and lease-expiry recovery never sees it",
			leases, durable)
	}
}

// RunReadyClaimerAnswersTheQuestionReaderReadyLists pins the filter's
// provenance (issueops/readyclaimer.go:9-14): the claim's predicate is the
// listing's predicate, because the request type IS Reader.Ready's request type
// and there is one builder behind both. Asserting it costs one shared seed and
// it encodes the rule that motivates the whole clause — a claim must not hand an
// agent work the listing never offered it.
//
// The high-priority outsider is the half that makes the agreement mean
// something. It would win an unfiltered claim, so a request whose filter was
// dropped or widened anywhere between the two surfaces takes it instead, and
// this case says so.
func RunReadyClaimerAnswersTheQuestionReaderReadyLists(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcagree"
	selected := fixture.IssuePrefix + "-rcagree-a"
	wrongPriority := fixture.IssuePrefix + "-rcagree-b"
	outsider := fixture.IssuePrefix + "-rcagree-outsider"
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(selected, 1, label))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(wrongPriority, 2, label))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(outsider, 0, label+"-outsider"))

	priority := 1
	request := publicops.ReadyRequest{Labels: []string{label}, Priority: &priority, Sort: readyClaimerSort}
	page, err := fixture.Reader.Ready(ctx, request)
	if err != nil {
		t.Fatalf("Reader.Ready: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("Reader.Ready listed nothing for a filter matching a seeded ready issue")
	}
	listedFirst := page.Items[0].ID

	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{Actor: "claimer", Filter: request})
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if result.Claimed == nil {
		t.Fatalf("ClaimNext returned nothing for the filter Reader.Ready answered with %v", readyClaimerPageIDs(page))
	}
	if result.Claimed.ID != listedFirst {
		t.Errorf("claimed %s but Reader.Ready listed %s first for the same request; "+
			"the claim's predicate and the listing's predicate must be one predicate", result.Claimed.ID, listedFirst)
	}
	if result.Claimed.ID != selected {
		t.Errorf("claimed %s, want %s", result.Claimed.ID, selected)
	}
	assertReadyClaimerRowState(t, ctx, fixture, outsider, string(types.StatusOpen), "")
}

// RunReadyClaimerSkipsIneligibleFrontRows makes the unbounded-scan rationale
// (issueops/readyclaimer.go:17-22) observable without a race. The top of the
// ready order is occupied by rows this actor cannot take — they are already
// assigned — and the claim must walk past them to the first row it CAN take
// rather than reporting an empty front.
//
// This is also where the claim's eligibility is narrower than the listing's:
// `bd ready` offers assigned open work, and the claim does not take it. Both
// halves are asserted, so a change that made the claim steal an assigned row
// fails here rather than in production.
func RunReadyClaimerSkipsIneligibleFrontRows(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rcskip"
	takenFirst := fixture.IssuePrefix + "-rcskip-a"
	takenSecond := fixture.IssuePrefix + "-rcskip-b"
	eligible := fixture.IssuePrefix + "-rcskip-c"

	first := readyClaimerIssue(takenFirst, 0, label)
	first.Assignee = "other-agent"
	second := readyClaimerIssue(takenSecond, 1, label)
	second.Assignee = "other-agent"
	seedReadyClaimerIssue(t, ctx, fixture, first)
	seedReadyClaimerIssue(t, ctx, fixture, second)
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(eligible, 2, label))

	request := publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort}
	page, err := fixture.Reader.Ready(ctx, request)
	if err != nil {
		t.Fatalf("Reader.Ready: %v", err)
	}
	if !readyClaimerPageHas(page, takenFirst) {
		t.Fatalf("Reader.Ready did not offer the assigned row %s (page: %v); "+
			"the case needs an ineligible row AT THE FRONT to have anything to skip", takenFirst, readyClaimerPageIDs(page))
	}

	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{Actor: "claimer", Filter: request})
	if err != nil {
		t.Fatalf("ClaimNext over a front whose first rows are ineligible: %v", err)
	}
	if result.Claimed == nil {
		t.Fatal("ClaimNext returned nothing while an eligible row sat behind two ineligible ones")
	}
	if result.Claimed.ID != eligible {
		t.Fatalf("claimed %s, want %s — the first ELIGIBLE row", result.Claimed.ID, eligible)
	}
	assertReadyClaimerRowState(t, ctx, fixture, takenFirst, string(types.StatusOpen), "other-agent")
	assertReadyClaimerRowState(t, ctx, fixture, takenSecond, string(types.StatusOpen), "other-agent")
}

// RunReadyClaimerRecordsOneHistoryEntryForAWin pins the counting half of "at
// most one durable history entry [per call], and none at all when nothing was
// eligible" (issueops/readyclaimer.go:71-76). The empty-front half lives in
// RunReadyClaimerEmptyFrontIsNormal, where the call that records nothing is the
// subject; the ephemeral half — a win that also records nothing, because
// ephemeral rows are not versioned — lives in
// RunReadyClaimerClaimsAnEphemeralRowTheFilterAdmits; here the subject is the
// DURABLE win, which records exactly one.
//
// It counts a DELTA around the operation rather than reading the top of the
// log: two commits made inside the same second tie on date, and their relative
// order is not something to rely on. What the entry SAYS — the commit-message
// spelling — is deliberately not here; that is single-sourced in
// internal/storage/issueops and pinning it three times would be duplication.
func RunReadyClaimerRecordsOneHistoryEntryForAWin(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe durable history, so the entry-per-call clause is unobservable on this backend")
	}
	label := fixture.IssuePrefix + "-rchist"
	winner := fixture.IssuePrefix + "-rchist-a"
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(winner, 0, label))

	before := readyClaimerHistoryCount(t, ctx, fixture)
	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{
		Actor:  "claimer",
		Filter: publicops.ReadyRequest{Labels: []string{label}, Sort: readyClaimerSort},
	})
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if result.Claimed == nil {
		t.Fatal("ClaimNext returned nothing against a front holding one claimable issue")
	}
	assertReadyClaimerHistoryDelta(t, ctx, fixture, before, 1, "a winning claim")
}

// RunReadyClaimerDoesNotMutateTheCallerRequest is the request-snapshot
// tripwire the leaf promises in so many words: "Implementations never mutate
// caller-owned request values, snapshot the request at method entry, and apply
// validation and normalization only to attempt-local clones"
// (issueops/readyclaimer.go:52-56).
//
// The request is passed by value, so only its REFERENCE members can carry a
// mutation back to the caller — the four label slices, ExcludeTypes,
// MetadataFields and the Priority pointer. Every one of them is populated here
// with a value normalization would want to touch, and the comparison is against
// a second copy built by the same function, so an in-place trim, dedupe, sort,
// alias expansion or write-through fails.
//
// The filter matches nothing on purpose. The promise is about the request, not
// about the outcome, and an empty front is the one outcome that is guaranteed
// not to depend on what the rest of the suite has seeded.
func RunReadyClaimerDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	build := func() publicops.ClaimNextRequest {
		priority := 2
		return publicops.ClaimNextRequest{
			Actor: "claimer",
			Filter: publicops.ReadyRequest{
				IssueType:      " Task ",
				Labels:         []string{fixture.IssuePrefix + "-rcsnap ", fixture.IssuePrefix + "-rcsnap "},
				LabelsAny:      []string{" " + fixture.IssuePrefix + "-rcsnap-any"},
				ExcludeLabels:  []string{fixture.IssuePrefix + "-rcsnap-not "},
				ExcludeTypes:   []string{"mr,epic", " chore "},
				MetadataFields: map[string]string{"team": "conformance"},
				HasMetadataKey: "team",
				Priority:       &priority,
				Sort:           readyClaimerSort,
			},
		}
	}
	request := build()
	want := build()

	if _, err := fixture.Claimer.ClaimNext(ctx, request); err != nil {
		t.Fatalf("ClaimNext with a fully populated filter: %v", err)
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("ClaimNext mutated the caller's request:\n got %+v\nwant %+v", request, want)
	}
}

// RunReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven pins the
// clause that makes ClaimNext safe to point at a lane: the filter it is given
// is the filter it claims through (issueops/readyclaimer.go:9-14), so a request
// that fences the ready set to one lane never returns work from another.
//
// THIS IS THE DROPPED-FILTER BUG, and it is the reason the case is shaped the
// way it is. --label-any used to be discarded on the ready/claim path, so a
// worker asking for its own lane atomically claimed another lane's work while
// believing it was fenced — a failure that looks exactly like success at every
// surface a caller can see. Nothing in this contract can see it today:
// LabelsAny appears only in RunReadyClaimerDoesNotMutateTheCallerRequest, where
// it is populated and never asserted as a filter, and the two cases that do
// fence fence with Labels alone.
//
// EVERY SEEDED ROW CARRIES THE CASE'S SCOPE LABEL and every request names it in
// Labels, because the ready front is a property of the whole database. The
// FENCE under test is therefore LabelsAny and ParentID, layered on top of that
// scope — which is also what makes the unsatisfiable-AND arm meaningful, since
// an implementation that let the OR-set stand in for the AND-set answers a row
// there.
//
// THE DECOY IS TOP-PRIORITY AND IN NO LANE. Every arm below is a claim that
// must NOT take it, so each one doubles as a check that the fence was applied
// at all rather than a check that some row came back. The requested order is
// `priority`, so "the fence was dropped" and "the fence was honored" name
// different rows every time.
//
// FOUR THINGS FAIL INDEPENDENTLY and each has an arm:
//
//   - the OR-set is an OR. The first request names a lane that matches nothing
//     alongside the one that does; an implementation reading LabelsAny as a
//     second AND-set claims nothing.
//   - the AND-set still binds. An unsatisfiable Labels entry beside a
//     satisfiable LabelsAny must claim NOTHING, not the row the OR-set alone
//     would have matched.
//   - the parent restricts, through BOTH of its arms. The parent clause is a
//     disjunction — a dotted-id descendant that owns no parent-child edge, OR a
//     row the recursive descendant walk reached — and the two are separate
//     bodies of code (issueops.GetDescendantIDsInTx and the unit of work's
//     hand-copied getDescendantIDs). One row of each shape is seeded, and the
//     second claim is what proves the walk arm is live.
//   - an EXHAUSTED fence claims nothing. This is the safety property: the
//     failure mode worth preventing is not an empty answer, it is a FULL one
//     from outside the lane. The final unfenced claim takes the decoy, so the
//     nils above are the fence's doing and not a drained front.
//
// WHAT THIS FIXTURE CANNOT SEE: ExcludeLabels, LabelPattern and LabelRegex,
// which are three more members of the same fence. They are not what the case is
// named for — the historical defect and the leaf's clause are both about the
// filter reaching the claim path at all, and a request that carries five
// predicates through the same builder does not carry three of them and drop
// two. Adding them would lengthen the case without adding a failure mode.
func RunReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	scope := fixture.IssuePrefix + "-rcfence"
	laneA := scope + "-lane-a"
	laneB := scope + "-lane-b"
	emptyLane := scope + "-lane-c"
	nobody := scope + "-nobody"

	decoy := scope + "-free"
	root := scope + "-p"
	wrongLaneChild := scope + "-p.1"
	dottedChild := scope + "-p.2"
	walkedChild := scope + "-pchild"
	outsider := scope + "-x"

	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(decoy, 0, scope))
	// The root sits at the BACK of the scope's order (priorities run 0..4), so
	// the unfenced claim that closes the case takes the decoy rather than it.
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(root, 4, scope))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(wrongLaneChild, 1, scope, laneB))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(outsider, 2, scope, laneA))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(dottedChild, 3, scope, laneA))
	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(walkedChild, 4, scope, laneA))
	// walkedChild is a descendant of root only through this edge; dottedChild
	// is one only through its id. The parent clause is a disjunction of those
	// two shapes and this case drives both.
	seedReadyClaimerTypedEdge(t, ctx, fixture, walkedChild, root, types.DepParentChild)

	scoped := publicops.ReadyRequest{Labels: []string{scope}, Sort: readyClaimerSort}
	page, err := fixture.Reader.Ready(ctx, scoped)
	if err != nil {
		t.Fatalf("Reader.Ready over the seeded scope: %v", err)
	}
	for _, id := range []string{decoy, root, wrongLaneChild, outsider, dottedChild, walkedChild} {
		if !readyClaimerPageHas(page, id) {
			t.Fatalf("Reader.Ready did not offer the seeded row %s (page: %v); every arm below is about WHICH ready row "+
				"the fence selects, so a row missing from the front makes its arm vacuous", id, readyClaimerPageIDs(page))
		}
	}

	// The OR-set narrows, the parent narrows, and the two compose: the only
	// lane-A descendant of root, ahead of a lane-A row outside it and a
	// higher-priority child in the wrong lane.
	inLane := publicops.ReadyRequest{
		Labels:    []string{scope},
		LabelsAny: []string{laneA, emptyLane},
		ParentID:  root,
		Sort:      readyClaimerSort,
	}
	if won := readyClaimerWin(t, ctx, fixture, inLane); won != dottedChild {
		t.Fatalf("ClaimNext(labels-any %v, parent %s) claimed %s, want %s — %s is the wrong lane, %s is outside the "+
			"parent, and %s is fenced out by both", []string{laneA, emptyLane}, root, won, dottedChild,
			wrongLaneChild, outsider, decoy)
	}
	// The parent clause's OTHER arm: this row is a descendant only through the
	// parent-child edge, so a body whose descendant walk went missing answers
	// nil here while the dotted-id arm above still passed.
	if won := readyClaimerWin(t, ctx, fixture, inLane); won != walkedChild {
		t.Fatalf("the second ClaimNext(parent %s) claimed %s, want %s — that row is a descendant only through its "+
			"parent-child edge, which is the arm of the parent clause the dotted id above does not exercise",
			root, won, walkedChild)
	}

	// The AND-set still binds beside a satisfiable OR-set.
	unsatisfiable := publicops.ReadyRequest{
		Labels:    []string{scope, nobody},
		LabelsAny: []string{laneA},
		Sort:      readyClaimerSort,
	}
	assertReadyClaimerClaimsNothing(t, ctx, fixture, unsatisfiable,
		"an unsatisfiable AND-set beside a satisfiable OR-set")

	// The OR-set alone still fences: the lane row, not the top-priority decoy.
	laneOnly := publicops.ReadyRequest{Labels: []string{scope}, LabelsAny: []string{laneA}, Sort: readyClaimerSort}
	if won := readyClaimerWin(t, ctx, fixture, laneOnly); won != outsider {
		t.Fatalf("ClaimNext(labels-any %s) claimed %s, want %s — %s is higher priority and in no lane, so claiming it "+
			"is the dropped-filter failure this case exists for", laneA, won, outsider, decoy)
	}

	// THE SAFETY PROPERTY: with the lane drained, the claim takes nothing
	// rather than falling back to unfenced work.
	assertReadyClaimerClaimsNothing(t, ctx, fixture, laneOnly, "an exhausted lane")

	// And the rows were claimable all along, so every nil above was the
	// fence's doing.
	if won := readyClaimerWin(t, ctx, fixture, scoped); won != decoy {
		t.Errorf("the unfenced claim took %s, want %s — the refusals above only mean something while this row is still claimable",
			won, decoy)
	}
}

// RunReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities pins what the
// two numbers on a claimed row COUNT. ClaimNext hydrates the row it won
// (RunReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState asserts they are
// populated from real edges), but every fixture in this contract seeds
// `blocks` edges and nothing else, so a body that counted every edge type
// answers exactly the same numbers.
//
// The two count sets are genuinely different sets. `bd show`'s dependent count
// is all-types (storage.CountDependents), while the cardinalities carried on a
// work row are blocks-only (sqlbuild.SearchCountsSQL's dc/rc joins both say
// `WHERE type = 'blocks'`), and the unit of work computes them in a body of its
// own. Nothing anywhere tells the two apart at this seam: swap them and every
// count assertion in this suite still passes.
//
// THE WINNER SITS AT THE CENTER OF SIX EDGES, three out and three in, one pair
// per type — and the raw edge counts are asserted FIRST. That is what makes the
// answer falsifiable rather than merely small: without them, "DependencyCount
// is 1" is equally consistent with a body that counts only blocks edges and
// with a fixture whose extra edges were never written. The case says three
// edges exist and one is counted.
//
// THE NON-BLOCKS EDGES ARE CHOSEN NOT TO MOVE THE ROW OFF THE READY FRONT: the
// blocks target is CLOSED, relates-to never gates, and a parent-child edge
// gates a child only through a parent that is itself blocked. So the winner
// stays claimable and the case is about the counts rather than about readiness,
// which the blocker-aware cases already own.
//
// WHAT THIS FIXTURE CANNOT SEE: the all-types count itself, which no verb on
// this role reports — the raw edge total stands in for it. That is not what the
// case is named for; the promise here is that the claim's cardinalities are the
// blocks-only ones, and a body answering 3 fails on exactly that.
func RunReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) {
	t.Helper()
	scope := fixture.IssuePrefix + "-rccount"
	offstage := scope + "-off"
	winner := scope + "-a"
	blocker := scope + "-blocker"
	dependent := scope + "-dependent"
	relatedOut := scope + "-related-out"
	relatedIn := scope + "-related-in"
	parent := scope + "-parent"
	child := scope + "-child"

	seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(winner, 0, scope))
	// A CLOSED blocks target: it gives the winner a counted dependency without
	// taking it off the ready front.
	closed := readyClaimerIssue(blocker, 1, offstage)
	closed.Status = types.StatusClosed
	seedReadyClaimerIssue(t, ctx, fixture, closed)
	for _, id := range []string{dependent, relatedOut, relatedIn, parent, child} {
		seedReadyClaimerIssue(t, ctx, fixture, readyClaimerIssue(id, 1, offstage))
	}

	seedReadyClaimerTypedEdge(t, ctx, fixture, winner, blocker, types.DepBlocks)
	seedReadyClaimerTypedEdge(t, ctx, fixture, winner, relatedOut, types.DepRelatesTo)
	seedReadyClaimerTypedEdge(t, ctx, fixture, winner, parent, types.DepParentChild)
	seedReadyClaimerTypedEdge(t, ctx, fixture, dependent, winner, types.DepBlocks)
	seedReadyClaimerTypedEdge(t, ctx, fixture, relatedIn, winner, types.DepRelatesTo)
	seedReadyClaimerTypedEdge(t, ctx, fixture, child, winner, types.DepParentChild)

	// Three edges each way really are on the row. Without this the equalities
	// below hold just as well over a fixture that wrote one edge of each.
	if out := readyClaimerEdgesFrom(t, ctx, fixture, winner); out != 3 {
		t.Fatalf("%s carries %d outgoing edges, want the 3 this case seeded; the blocks-only assertion below cannot "+
			"fail while the other two types are missing", winner, out)
	}
	if in := readyClaimerEdgesTo(t, ctx, fixture, winner); in != 3 {
		t.Fatalf("%s carries %d incoming edges, want the 3 this case seeded", winner, in)
	}

	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{
		Actor:  "claimer",
		Filter: publicops.ReadyRequest{Labels: []string{scope}, Sort: readyClaimerSort},
	})
	if err != nil {
		t.Fatalf("ClaimNext over the seeded scope: %v", err)
	}
	if result.Claimed == nil {
		t.Fatal("ClaimNext returned no row against a front holding one claimable issue")
	}
	if result.Claimed.ID != winner {
		t.Fatalf("claimed %s, want %s", result.Claimed.ID, winner)
	}
	if result.Claimed.DependencyCount != 1 {
		t.Errorf("returned DependencyCount = %d over 3 outgoing edges (one blocks, one relates-to, one parent-child), "+
			"want 1 — a work row's cardinalities count blocks edges only", result.Claimed.DependencyCount)
	}
	if result.Claimed.DependentCount != 1 {
		t.Errorf("returned DependentCount = %d over 3 incoming edges (one blocks, one relates-to, one parent-child), "+
			"want 1 — a work row's cardinalities count blocks edges only", result.Claimed.DependentCount)
	}
}

// assertReadyClaimerClaimsNothing runs one claim that must take no row and says
// which row it took when it does, because "the fence was dropped" is only
// diagnosable from the id.
func assertReadyClaimerClaimsNothing(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, filter publicops.ReadyRequest, subject string) {
	t.Helper()
	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{Actor: "claimer", Filter: filter})
	if err != nil {
		t.Fatalf("ClaimNext with %s: error = %v, want nil — a fence that matches nothing is an EMPTY front, not a failure", subject, err)
	}
	if result.Claimed != nil {
		t.Fatalf("ClaimNext with %s claimed %s; a claim that falls back past its own fence hands an agent work from "+
			"another lane while the caller believes it is fenced", subject, result.Claimed.ID)
	}
}

// seedReadyClaimerTypedEdge seeds ONE edge of a named type.
// seedReadyClaimerEdge is the blocks-only shorthand every other case uses; the
// cases that are ABOUT edge type need to say which.
func seedReadyClaimerTypedEdge(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, issueID, dependsOnID string, depType types.DependencyType) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: issueID, DependsOnID: dependsOnID, Type: depType,
	}, "seed"); err != nil {
		t.Fatalf("seed %s edge %s -> %s: %v", depType, issueID, dependsOnID, err)
	}
}

// readyClaimerEdgesFrom counts every dependency row leaving id, whatever its
// type. It is the all-types control the role itself never reports.
func readyClaimerEdgesFrom(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, id string) int {
	t.Helper()
	var rows int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", []any{id}, &rows); err != nil {
		t.Fatalf("count outgoing edges for %s: %v", id, err)
	}
	return rows
}

// readyClaimerEdgesTo counts every dependency row arriving at id. The target's
// own class decides which typed column holds it, so it is resolved through the
// same COALESCE the rest of the contract family uses.
func readyClaimerEdgesTo(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, id string) int {
	t.Helper()
	var rows int
	if err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		[]any{id}, &rows); err != nil {
		t.Fatalf("count incoming edges for %s: %v", id, err)
	}
	return rows
}

// readyClaimerSort is the order every case names. It is the policy whose SQL is
// a plain "ORDER BY priority, created_at, id", so a seed with distinct
// priorities has one unambiguous front row — hybrid's recency bucket would make
// the winner depend on when the fixture was seeded.
const readyClaimerSort = "priority"

func readyClaimerIssue(id string, priority int, labels ...string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: types.TypeTask,
		Labels:    labels,
	}
}

func seedReadyClaimerIssue(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func seedReadyClaimerWisp(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, issue *types.Issue) {
	t.Helper()
	issue.Ephemeral = true
	if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", issue.ID, err)
	}
}

func seedReadyClaimerEdge(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, issueID, dependsOnID string) {
	t.Helper()
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID: issueID, DependsOnID: dependsOnID, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed edge %s -> %s: %v", issueID, dependsOnID, err)
	}
}

// assertReadyClaimerRowState reads the durable row's status and assignee back
// out of the database. The assignee comes through COALESCE because an unclaimed
// row's column may be NULL or empty depending on how it was seeded, and the
// three fixtures' scan paths do not agree on what a NULL means.
func assertReadyClaimerRowState(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, id, wantStatus, wantAssignee string) {
	t.Helper()
	assertReadyClaimerPlaneRowState(t, ctx, fixture, "issues", id, wantStatus, wantAssignee)
}

func assertReadyClaimerWispState(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, id, wantStatus, wantAssignee string) {
	t.Helper()
	assertReadyClaimerPlaneRowState(t, ctx, fixture, "wisps", id, wantStatus, wantAssignee)
}

// assertReadyClaimerRowAbsent checks that a plane holds no row for id. It
// counts rather than scanning one, so "absent" and "present but unreadable"
// cannot be confused: a failed scan would be a fixture error, an empty count is
// the answer.
func assertReadyClaimerRowAbsent(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, table, id string) {
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

func assertReadyClaimerPlaneRowState(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, table, id, wantStatus, wantAssignee string) {
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

// readyClaimerWin runs one claim that is expected to take a row and reports the
// id it won. A nil Claimed is fatal here rather than a returned empty string,
// because every caller of this helper is asserting something ABOUT the win.
func readyClaimerWin(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, filter publicops.ReadyRequest) string {
	t.Helper()
	result, err := fixture.Claimer.ClaimNext(ctx, publicops.ClaimNextRequest{Actor: "claimer", Filter: filter})
	if err != nil {
		t.Fatalf("ClaimNext(labels=%v, includeEphemeral=%v): %v", filter.Labels, filter.IncludeEphemeral, err)
	}
	if result.Claimed == nil {
		t.Fatalf("ClaimNext(labels=%v, includeEphemeral=%v) returned no row against a front seeded to hold one",
			filter.Labels, filter.IncludeEphemeral)
	}
	return result.Claimed.ID
}

// readyClaimerLeaseCount reports how many lease rows the ephemeral leases table
// holds for id. The table is a plain SQL table on every backend
// (internal/storage/schema/cli_migrations.go:108-115), reached here through the
// same QueryScalar the row-state assertions use.
func readyClaimerLeaseCount(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, id string) int {
	t.Helper()
	var rows int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", []any{id}, &rows); err != nil {
		t.Fatalf("count leases for %s: %v", id, err)
	}
	return rows
}

func readyClaimerHistoryCount(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture) int {
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

// assertReadyClaimerHistoryDelta checks how far the durable history moved.
// A fixture that cannot observe history reports -1 from
// readyClaimerHistoryCount and the check is skipped there rather than passing
// on an arithmetic accident.
func assertReadyClaimerHistoryDelta(t *testing.T, ctx context.Context, fixture ReadyClaimerFixture, before, want int, subject string) {
	t.Helper()
	if before < 0 {
		return
	}
	after := readyClaimerHistoryCount(t, ctx, fixture)
	if after-before != want {
		t.Errorf("history entries went %d -> %d across %s, want exactly %d more", before, after, subject, want)
	}
}

func readyClaimerPageHas(page publicops.IssuePage, id string) bool {
	for _, item := range page.Items {
		if item != nil && item.ID == id {
			return true
		}
	}
	return false
}

func readyClaimerPageIDs(page publicops.IssuePage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item != nil {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
