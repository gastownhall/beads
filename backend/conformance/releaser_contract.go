package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.Releaser must satisfy. Each case asserts what issueops/releaser.go
// PROMISES, cited by symbol, rather than what any one backend happens to do; a
// backend that genuinely disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// THERE IS ONE BODY BEHIND THE THREE WIRINGS. The two stores wrap
// internal/storage/issueops.ReleaseIssueInTx in their own transaction and the
// unit-of-work provider reaches the SAME function through the domain issue
// repository, so a case passing three times is ONE reading plus two wrapper
// checks — never three independent votes. The cases are written for that: they
// assert SENTINELS rather than message text and RAW ROWS rather than the role's
// own answer, because what a per-leg failure would actually be is a wrapper
// losing a transaction, dropping a request field, or breaking errors.Is.
//
// The wrapper check is not theoretical on this role. Each leg decides for
// itself what to VERSION, and the fact that separates them is the one
// MetadataCASWrite was invented for: an ephemeral release writes a row and
// changes no durable table, and a leg that reads an empty table set as "nothing
// happened" rolls the write back. RunReleaserReleasesAWispClaimWithoutVersioning
// is the case that can tell the three apart, and it is the reason a wisp arm
// exists at all.
//
// STATE IS READ BACK AS RAW ROWS, never through the role. "Every refusal leaves
// persistent state unchanged" is the clause a role-answer assertion cannot
// check: reading the row back through the thing under test is exactly the check
// that passes on a corrupted table.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN, and why:
//
//   - ReleaseResult.Changed being FALSE. Nothing returns it false: the role
//     refuses every shape that would not write, which is what its own doc says.
//     A case named for the false answer could not fail, and a green case named
//     for a promise is worse than no case — a reviewer greps for the promise,
//     finds a test, and stops looking. What the cases below DO assert is that
//     Changed is TRUE on every release that landed, which goes red against a
//     body that hardcodes the zero value. The decorator-level consequence of the
//     field — that a hook fires on Changed and not on the absence of an error —
//     is pinned in internal/storage/hook_releaser_test.go, where a false answer
//     can be driven.
//   - "It is ONE TRANSACTION." Structural, not black-box observable: one
//     transaction and two produce identical answers when nothing else is
//     writing, and a concurrent case would be flaky at three engines. What
//     holds it is the SHAPE of the body — there is no two-call composition to
//     regress into without deleting ReleaseIssueInTx. A transaction-counting
//     seam on the fixture kit would upgrade it.
//   - "Implementations own transaction retry." Same reason, and it is pinned by
//     the shape of each leg (withRetryTx / RunTxResult) and by review.
//   - "a detached post-state snapshot". Detachment is not observable across the
//     seam; what IS observable — that the snapshot equals the row left in the
//     database — is asserted instead.
//
// EVERY CASE NAMESPACES ITS SEEDS with fixture.IssuePrefix and its own tag: the
// three wirings share one database across the whole role suite.

// ReleaserFixture supplies adapter-specific storage access for the
// claim-release assertions. Every field is named and typed exactly like the
// per-backend roleFixtureKit hook it is filled from.
type ReleaserFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Releaser is the surface under test.
	Releaser publicops.Releaser
	// CreateIssue seeds a durable issue in the issues plane. Cases seed
	// Assignee, Status and StartedAt directly through it: a CLAIM is a property
	// of the row, and this role only ever takes one away.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane, for the arm that
	// is about the plane rather than the row.
	CreateWisp func(context.Context, *types.Issue, string) error
	// QueryScalar runs a single-row query and scans it. It is how these cases
	// read RAW ROWS — the only way to tell "the answer looks right" from "the
	// table is right".
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the cases
	// that need it SKIP with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
	// CommitPending puts everything written so far into the version history, so
	// a later CountHistory delta measures the call under test and nothing that
	// led up to it. It is a PRECONDITION of the history cases rather than a
	// convenience, for the reason DeleterFixture.CommitPending gives at length:
	// two of the three kits version each seed as a side effect of writing it
	// and one does not, so without it the same case measures different things
	// on different legs. A nil hook skips those cases LOUDLY.
	CommitPending func(context.Context) error
}

// RunReleaserReleasesItsOwnClaim pins the whole post-state
// (issueops/releaser.go, Releaser: "assignee is cleared, status becomes the
// literal StatusOpen, started_at is cleared, the row's lease is deleted, and
// RowVersion is reminted").
//
// EVERY ONE OF THOSE IS READ RAW, because the point of stating the post-state
// on the role was that a caller should not have to spell it as a patch. A case
// that only asked whether the assignee went would pass over an implementation
// that left the issue in_progress and unassigned — a row no claim path can take
// again and no report explains.
//
// THE ROW VERSION IS ASSERTED TO MOVE, not to hold a value: the token is minted
// fresh rather than incremented, so "different" is the whole promise, and it is
// the promise a caller's next ExpectedVersion depends on. It is also read back
// off ReleaseResult.Issue, which is what the role tells a caller to feed
// forward — a snapshot carrying a token the row does not have is the defect
// that makes the documented compose-and-continue loop diverge.
func RunReleaserReleasesItsOwnClaim(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	id := releaserSeedClaimed(t, ctx, fixture, "own", "target", false)
	before := releaserRowVersion(t, ctx, fixture, id)
	if started := releaserScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM issues WHERE id = ? AND started_at IS NOT NULL", id); started != 1 {
		t.Fatalf("seed left started_at NULL; the cleared-started_at clause below could not fail")
	}

	result, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{Actor: releaserHolder, IssueID: id})
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if !result.Changed {
		t.Errorf("Changed = false, want true for a release that wrote the row")
	}
	if result.Issue == nil {
		t.Fatalf("Issue = nil, want the post-release row")
	}

	releaserAssertRow(t, ctx, fixture, "issues", id, "", types.StatusOpen)
	if started := releaserScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM issues WHERE id = ? AND started_at IS NULL", id); started != 1 {
		t.Errorf("started_at survived the release; a row released but still 'started' is a claim no verb can see")
	}
	if leases := releaserScalar[int](t, ctx, fixture,
		"SELECT COUNT(*) FROM leases WHERE issue_id = ?", id); leases != 0 {
		t.Errorf("lease rows for %s = %d, want 0 — a released row holding a lease is the state a reaper cannot fix", id, leases)
	}

	after := releaserRowVersion(t, ctx, fixture, id)
	if after == before {
		t.Errorf("row version did not move across the release; a concurrent reclaim would silently merge with it")
	}
	if result.Issue.RowVersion != after {
		t.Errorf("Issue.RowVersion = %d, want the row's %d — the token a caller feeds forward must be the row's",
			result.Issue.RowVersion, after)
	}
	if result.Issue.Assignee != "" || result.Issue.Status != types.StatusOpen {
		t.Errorf("snapshot = assignee %q status %q, want the released row", result.Issue.Assignee, result.Issue.Status)
	}
}

// RunReleaserRefusesAForeignClaimUntilForced pins the ownership fence and the
// one thing Force does (issueops/releaser.go, ReleaseRequest.Force: "bypasses
// the ownership fence, so an actor that is not the holder may release the
// claim").
//
// THE FORCED HALF IS WHAT MAKES THE REFUSAL FALSIFIABLE. A body that refused
// every foreign release, forced or not, would pass a refusal-only case
// perfectly — and refusal-only coverage of a guarded write is half a test.
func RunReleaserRefusesAForeignClaimUntilForced(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	id := releaserSeedClaimed(t, ctx, fixture, "foreign", "target", false)
	version := releaserRowVersion(t, ctx, fixture, id)

	_, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{Actor: "releaser-stranger", IssueID: id})
	if !errors.Is(err, publicops.ErrNotOwner) {
		t.Fatalf("Release() by a stranger error = %v, want ErrNotOwner", err)
	}
	releaserAssertRow(t, ctx, fixture, "issues", id, releaserHolder, types.StatusInProgress)
	if got := releaserRowVersion(t, ctx, fixture, id); got != version {
		t.Errorf("a refused release moved the row version; the holder's next compare-and-set would lose for a reason it cannot see")
	}

	result, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{
		Actor: "releaser-reaper", IssueID: id, Force: true,
	})
	if err != nil {
		t.Fatalf("forced Release() error = %v", err)
	}
	if !result.Changed {
		t.Errorf("Changed = false, want true for a forced release that wrote the row")
	}
	releaserAssertRow(t, ctx, fixture, "issues", id, "", types.StatusOpen)
}

// RunReleaserReleasesOnlyTheExpectedHolder pins the compare-and-set
// (issueops/releaser.go, ReleaseRequest.ExpectedAssignee).
//
// BOTH LIMBS, and they are seeded on TWO rows so neither depends on the other's
// leftovers. The mismatch limb is the one whose row is read raw afterwards: a
// conditional release that clobbered a claim that had moved is precisely the
// bug the compare-and-set exists to prevent, and "it returned an error" is not
// the same statement as "the claim is still there".
//
// THE MATCHING LIMB'S ACTOR IS NOT THE HOLDER, which is the clause a reader is
// most likely to get backwards: a match REPLACES the ownership fence, so the
// caller need not be the holder. An implementation that ran the fence anyway
// would answer ErrNotOwner here and pass every other case in this file.
//
// THE LAST TWO LIMBS PIN WHAT "MATCH" MEANS, which nothing at this tier pinned
// before (ga-2ltro.14) — and it is not string equality. The comparison is
// SEPARATOR-INSENSITIVE AND NOTHING ELSE (canonicalActor, ga-5ksp5): a run of
// ".", "_" or "-" matches any other such run, so an expectation spelled under
// one layer's separator convention releases a claim stored under another's.
// The one exception is an exact "--" run: it is gascity's encoding of a
// rig-qualified agent's "/" and decodes to "/", so "a--b" matches "a/b" and
// not "a__b" (ga-2vy9p2). This tier does not pin that limb — the respelling
// exercised below is "-" -> "_", which is unaffected — but the contract text
// must not claim a rule the implementation no longer has.
// NOTHING ELSE IS FORGIVEN, and the padded limb is the half that says so: an
// implementation that reached for strings.TrimSpace to make the respelled limb
// pass would release on an expectation the caller never actually held.
//
// The two limbs are ONE case rather than two because they are one predicate,
// and a reader who sees only the forgiving half draws exactly the wrong
// conclusion about the other.
func RunReleaserReleasesOnlyTheExpectedHolder(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	stale := "releaser-previous-holder"

	moved := releaserSeedClaimed(t, ctx, fixture, "expect", "moved", false)
	version := releaserRowVersion(t, ctx, fixture, moved)
	_, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{
		Actor: "releaser-supervisor", IssueID: moved, ExpectedAssignee: &stale,
	})
	if !errors.Is(err, publicops.ErrAssigneeMismatch) {
		t.Fatalf("Release() naming a stale holder error = %v, want ErrAssigneeMismatch", err)
	}
	releaserAssertRow(t, ctx, fixture, "issues", moved, releaserHolder, types.StatusInProgress)
	if got := releaserRowVersion(t, ctx, fixture, moved); got != version {
		t.Errorf("a refused conditional release moved the row version")
	}

	current := releaserHolder
	matched := releaserSeedClaimed(t, ctx, fixture, "expect", "matched", false)
	result, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{
		Actor: "releaser-supervisor", IssueID: matched, ExpectedAssignee: &current,
	})
	if err != nil {
		t.Fatalf("Release() naming the current holder error = %v, want the release to land — a match replaces the ownership fence", err)
	}
	if !result.Changed {
		t.Errorf("Changed = false, want true for a conditional release that wrote the row")
	}
	releaserAssertRow(t, ctx, fixture, "issues", matched, "", types.StatusOpen)

	// releaserHolder spelled with the other separator: "releaser_holder"
	// against a row holding "releaser-holder". Both canonicalize to one
	// identity, so the release lands.
	respelled := strings.ReplaceAll(releaserHolder, "-", "_")
	if respelled == releaserHolder {
		t.Fatalf("releaserHolder %q carries no separator to respell — this limb would assert nothing", releaserHolder)
	}
	separatorSpelled := releaserSeedClaimed(t, ctx, fixture, "expect", "respelled", false)
	if _, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{
		Actor: "releaser-supervisor", IssueID: separatorSpelled, ExpectedAssignee: &respelled,
	}); err != nil {
		t.Fatalf("Release() expecting %q against a claim held by %q error = %v, want the release to land — the comparison is separator-insensitive",
			respelled, releaserHolder, err)
	}
	releaserAssertRow(t, ctx, fixture, "issues", separatorSpelled, "", types.StatusOpen)

	// The other half of the same predicate: separators are all that is
	// forgiven. A padded expectation is a different string and refuses, and the
	// claim it named is still there afterwards.
	padded := " " + releaserHolder
	untouched := releaserSeedClaimed(t, ctx, fixture, "expect", "padded", false)
	paddedVersion := releaserRowVersion(t, ctx, fixture, untouched)
	if _, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{
		Actor: "releaser-supervisor", IssueID: untouched, ExpectedAssignee: &padded,
	}); !errors.Is(err, publicops.ErrAssigneeMismatch) {
		t.Fatalf("Release() expecting %q error = %v, want ErrAssigneeMismatch — the value is not trimmed", padded, err)
	}
	releaserAssertRow(t, ctx, fixture, "issues", untouched, releaserHolder, types.StatusInProgress)
	if got := releaserRowVersion(t, ctx, fixture, untouched); got != paddedVersion {
		t.Errorf("a refused padded expectation moved the row version")
	}
}

// RunReleaserRefusesAnUnheldIssue pins the three answers an unheld row gets
// (issueops/releaser.go, ErrNotClaimed and Releaser.Release's refusal list).
//
// THE FORCED ARM IS THE ONE WORTH THE ROW. Force is the flag that exists to
// make refusals go away, so an implementation that reached for it here is
// entirely plausible — and a forced release of a row nobody holds is not a
// release, it is a caller being told nothing happened while believing it did.
//
// THE CONDITIONAL ARM ANSWERS DIFFERENTLY ON PURPOSE, and the case says which:
// a caller that named a holder gets ErrAssigneeMismatch rather than
// ErrNotClaimed, because it asked about a specific holder and the answer is
// that it is not the holder. Conflating the two would make a supervisor's
// retry loop unable to tell "somebody else released it" from "the row was never
// claimed".
func RunReleaserRefusesAnUnheldIssue(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	id := releaserSeed(t, ctx, fixture, releaserIssue(fixture, "unheld", "target", false))
	version := releaserRowVersion(t, ctx, fixture, id)
	expectedHolder := releaserHolder

	for _, test := range []struct {
		name    string
		request publicops.ReleaseRequest
		want    error
	}{
		{"unconditional", publicops.ReleaseRequest{Actor: releaserHolder, IssueID: id}, publicops.ErrNotClaimed},
		{"forced", publicops.ReleaseRequest{Actor: "releaser-reaper", IssueID: id, Force: true}, publicops.ErrNotClaimed},
		{"conditional", publicops.ReleaseRequest{
			Actor: "releaser-supervisor", IssueID: id, ExpectedAssignee: &expectedHolder,
		}, publicops.ErrAssigneeMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.Releaser.Release(ctx, test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Release(%s) error = %v, want %v", test.name, err, test.want)
			}
			releaserAssertRow(t, ctx, fixture, "issues", id, "", types.StatusOpen)
			if got := releaserRowVersion(t, ctx, fixture, id); got != version {
				t.Errorf("a refused release moved the row version")
			}
		})
	}
}

// RunReleaserRefusesAStatusThatCannotBeReleased pins the status refusal
// (issueops/releaser.go, ErrNotReleasable: "a closed issue, or an issue in any
// status other than the two the release transition is defined over").
//
// THE SECOND ROW IS THE ONE THE LEAF WENT OUT OF ITS WAY TO NAME, and it is a
// built-in status rather than an invented one: an issue that is BLOCKED while
// still holding a claim is an ordinary state a workspace reaches, and it is
// refused here. Before this role the same request failed with an untyped "no
// matching row" that no caller could classify and no message explained.
// Asserting the sentinel is what turns that from a story into a contract — and
// asserting the CLAIM SURVIVES is what says the refusal did not half-apply.
func RunReleaserRefusesAStatusThatCannotBeReleased(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()

	for _, test := range []struct {
		name   string
		status types.Status
	}{
		{"closed", types.StatusClosed},
		{"blocked", types.StatusBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			issue := releaserIssue(fixture, "status", test.name, false)
			issue.Status = test.status
			issue.Assignee = releaserHolder
			id := releaserSeed(t, ctx, fixture, issue)

			_, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{Actor: releaserHolder, IssueID: id})
			if !errors.Is(err, publicops.ErrNotReleasable) {
				t.Fatalf("Release() of a %s issue error = %v, want ErrNotReleasable", test.name, err)
			}
			releaserAssertRow(t, ctx, fixture, "issues", id, releaserHolder, test.status)
		})
	}
}

// RunReleaserRefusesAMalformedRequest pins the request rules that need no
// database (issueops/releaser.go, Releaser.Release: "ErrValidation, before
// anything is read").
//
// The two ExpectedAssignee rows are the ones that are not obvious from the
// field's type: an empty expectation is NOT "expected unassigned" here, unlike
// UpdateRequest.ExpectedAssignee, and Force beside an expectation is two
// answers to one question.
func RunReleaserRefusesAMalformedRequest(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	id := releaserSeedClaimed(t, ctx, fixture, "malformed", "target", false)
	empty := ""
	expectedHolder := releaserHolder

	for _, test := range []struct {
		name    string
		request publicops.ReleaseRequest
	}{
		{"no actor", publicops.ReleaseRequest{IssueID: id}},
		{"no issue id", publicops.ReleaseRequest{Actor: releaserHolder}},
		{"empty expected assignee", publicops.ReleaseRequest{
			Actor: releaserHolder, IssueID: id, ExpectedAssignee: &empty,
		}},
		{"force beside an expected assignee", publicops.ReleaseRequest{
			Actor: releaserHolder, IssueID: id, ExpectedAssignee: &expectedHolder, Force: true,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := fixture.Releaser.Release(ctx, test.request); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("Release(%s) error = %v, want ErrValidation", test.name, err)
			}
		})
	}
	// The claim is still there: a refused request reads nothing and writes
	// nothing, and the last two rows above name a real, claimed id.
	releaserAssertRow(t, ctx, fixture, "issues", id, releaserHolder, types.StatusInProgress)
}

// RunReleaserRefusesAnAbsentID pins the existence refusal
// (issueops/releaser.go, Releaser.Release: "an id naming no row in either
// plane: ErrNotFound").
//
// It runs the FORCED shape too, because force is the flag most likely to be
// read as "do it anyway" — and an id that names nothing is the one thing no
// flag can conjure.
func RunReleaserRefusesAnAbsentID(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	absent := fixture.IssuePrefix + "-absent-nosuchrow"
	for _, force := range []bool{false, true} {
		_, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{
			Actor: releaserHolder, IssueID: absent, Force: force,
		})
		if !errors.Is(err, publicops.ErrNotFound) {
			t.Fatalf("Release(force=%v) of an absent id error = %v, want ErrNotFound", force, err)
		}
	}
}

// RunReleaserAttributesTheReleaseToTheActor pins the attribution
// (issueops/releaser.go, ReleaseRequest.Actor: "attributes the release on the
// row's event history").
//
// THE RELEASE IS FORCED BY SOMEONE WHO IS NOT THE HOLDER, which is the only
// arrangement that can fail: when actor and holder are the same string, an
// implementation stamping the HOLDER on the entry is indistinguishable from one
// stamping the actor.
//
// THE ENTRIES ARE COUNTED PER ACTOR rather than read off the newest row.
// created_at here is second-granularity, so two entries written inside one
// second tie and an ORDER BY decides the case on a coin toss — the trap the
// metadata compare-and-set contract hit and documented.
func RunReleaserAttributesTheReleaseToTheActor(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	const actor = "releaser-reaper"
	id := releaserSeedClaimed(t, ctx, fixture, "attribution", "target", false)

	if _, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{
		Actor: actor, IssueID: id, Force: true,
	}); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	if got := releaserUnclaimEvents(t, ctx, fixture, id, actor); got != 1 {
		t.Errorf("unclaimed events attributed to %q = %d, want 1", actor, got)
	}
	if got := releaserUnclaimEvents(t, ctx, fixture, id, releaserHolder); got != 0 {
		t.Errorf("unclaimed events attributed to the HOLDER %q = %d, want 0 — the actor released it, not the holder", releaserHolder, got)
	}
}

// RunReleaserRecordsExactlyOneHistoryEntry pins the versioning promise: a
// release that landed is ONE act, so it records one entry, not one per table it
// touched and not none.
//
// CommitPending FIRST, for the reason DeleterFixture.CommitPending states:
// without it this measures a versioned release on two legs and an unversioned
// one on the third, and on that third it also depends on which other subtests
// ran before it.
func RunReleaserRecordsExactlyOneHistoryEntry(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	if fixture.CountHistory == nil || fixture.CommitPending == nil {
		t.Skip("this backend cannot observe or settle its version history")
	}
	id := releaserSeedClaimed(t, ctx, fixture, "history", "target", false)
	if err := fixture.CommitPending(ctx); err != nil {
		t.Fatalf("CommitPending(): %v", err)
	}

	before := releaserHistory(t, ctx, fixture)
	if _, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{Actor: releaserHolder, IssueID: id}); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if got := releaserHistory(t, ctx, fixture) - before; got != 1 {
		t.Errorf("history entries recorded by one release = %d, want 1", got)
	}
}

// RunReleaserReleasesAWispClaimWithoutVersioning is the case that can tell the
// three legs apart, and the reason a wisp arm exists at all.
//
// An EPHEMERAL release writes a row and changes no table this plane versions,
// so the two facts a body reports about a write — that it happened, and which
// durable tables moved — come apart here and nowhere else. A leg reading the
// empty table set as "nothing happened" rolls the write back and the wisp comes
// out still claimed, which is what happened to a compare-and-set on the
// unit-of-work leg before ReleaseWrite's ancestor separated the two.
//
// It also pins the plane routing: releasing a wisp is refused outright by
// Claimer's sibling promise ("a wisp id is ErrNotFound: the wisp plane is not
// claimable through this role"), and this role deliberately answers the other
// way, because a wisp that could be claimed and not released would strand work
// no verb could free.
func RunReleaserReleasesAWispClaimWithoutVersioning(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	id := releaserSeedClaimed(t, ctx, fixture, "wisp", "target", true)

	var before int
	settled := fixture.CountHistory != nil && fixture.CommitPending != nil
	if settled {
		if err := fixture.CommitPending(ctx); err != nil {
			t.Fatalf("CommitPending(): %v", err)
		}
		before = releaserHistory(t, ctx, fixture)
	}

	result, err := fixture.Releaser.Release(ctx, publicops.ReleaseRequest{Actor: releaserHolder, IssueID: id})
	if err != nil {
		t.Fatalf("Release() of a claimed wisp error = %v", err)
	}
	if !result.Changed {
		t.Errorf("Changed = false, want true — an ephemeral release still wrote a row")
	}
	releaserAssertRow(t, ctx, fixture, "wisps", id, "", types.StatusOpen)

	if settled {
		if got := releaserHistory(t, ctx, fixture) - before; got != 0 {
			t.Errorf("history entries recorded by an ephemeral release = %d, want 0 — the wisp tables are not versioned", got)
		}
	}
}

// RunReleaserDoesNotMutateTheCallerRequest pins the no-mutation promise
// (issueops/releaser.go, ReleaseRequest: "ExpectedAssignee is read, never
// written through").
//
// The pointer is the only member a callee could write through, and the natural
// way to normalize an expectation in place — trimming it — would hand the
// caller back a different string than it passed. The request is spelled with a
// MISMATCHING expectation so the refusing path is what gets exercised: a body
// that normalized before it discovered the request was doomed is where the
// mutation would survive unnoticed.
func RunReleaserDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture ReleaserFixture) {
	t.Helper()
	id := releaserSeedClaimed(t, ctx, fixture, "immutable", "target", false)
	expected := "releaser-previous-holder"
	request := publicops.ReleaseRequest{
		Actor: "releaser-supervisor", IssueID: id, ExpectedAssignee: &expected,
	}
	snapshot := request
	snapshotValue := expected

	if _, err := fixture.Releaser.Release(ctx, request); !errors.Is(err, publicops.ErrAssigneeMismatch) {
		t.Fatalf("Release() error = %v, want ErrAssigneeMismatch", err)
	}
	if expected != snapshotValue {
		t.Errorf("the caller's expected assignee changed across the call: got %q, want %q", expected, snapshotValue)
	}
	if request.Actor != snapshot.Actor || request.IssueID != snapshot.IssueID ||
		request.ExpectedAssignee != snapshot.ExpectedAssignee || request.Force != snapshot.Force {
		t.Errorf("the caller's request changed across the call: got %+v, want %+v", request, snapshot)
	}
}

// --- fixture helpers -------------------------------------------------------

func releaserIssue(fixture ReleaserFixture, tag, name string, ephemeral bool) *types.Issue {
	return &types.Issue{
		ID:        fmt.Sprintf("%s-%s-%s", fixture.IssuePrefix, tag, name),
		Title:     tag + " " + name,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
}

// releaserSeed writes one issue through the plane its Ephemeral flag names.
func releaserSeed(t *testing.T, ctx context.Context, fixture ReleaserFixture, issue *types.Issue) string {
	t.Helper()
	create := fixture.CreateIssue
	if issue.Ephemeral {
		create = fixture.CreateWisp
	}
	if err := create(ctx, issue, "releaser-seed"); err != nil {
		t.Fatalf("seeding %s: %v", issue.ID, err)
	}
	return issue.ID
}

// releaserHolder is the actor every case here seeds a claim to. It is one
// constant rather than a per-case string because half these cases are ABOUT the
// difference between the holder and the caller, and two spellings of "the
// holder" is how such a case comes to assert the wrong one.
const releaserHolder = "releaser-holder"

// releaserSeedClaimed seeds a row in the state a release acts on: held by
// releaserHolder, in progress and started.
//
// The claim is seeded on the ROW rather than taken through Claimer, which the
// fixture does not carry — and which would make every case here depend on a
// second role's behavior to establish its own precondition.
func releaserSeedClaimed(t *testing.T, ctx context.Context, fixture ReleaserFixture, tag, name string, ephemeral bool) string {
	t.Helper()
	started := time.Now().UTC().Add(-time.Hour)
	issue := releaserIssue(fixture, tag, name, ephemeral)
	issue.Status = types.StatusInProgress
	issue.Assignee = releaserHolder
	issue.StartedAt = &started
	return releaserSeed(t, ctx, fixture, issue)
}

// releaserAssertRow reads the two cells a release moves straight out of the
// named plane's table. It is the raw counterpart of every "the release landed"
// or "the refusal changed nothing" claim in this file.
//
//nolint:gosec // G201: table is chosen by the caller from the two plane tables.
func releaserAssertRow(t *testing.T, ctx context.Context, fixture ReleaserFixture, table, id, wantAssignee string, wantStatus types.Status) {
	t.Helper()
	var assignee, status string
	query := fmt.Sprintf("SELECT COALESCE(assignee, ''), status FROM %s WHERE id = ?", table)
	if err := fixture.QueryScalar(ctx, query, []any{id}, &assignee, &status); err != nil {
		t.Fatalf("reading %s from %s: %v", id, table, err)
	}
	if assignee != wantAssignee || status != string(wantStatus) {
		t.Errorf("%s row = assignee %q status %q, want assignee %q status %q",
			id, assignee, status, wantAssignee, wantStatus)
	}
}

// releaserRowVersion reads the RowVersion token straight off the DURABLE row.
// COALESCE mirrors the defensive scan the shared guard does, so a NULL in a
// NOT NULL DEFAULT 0 column reads as 0 rather than failing a case for a reason
// that is not its subject.
//
// It takes no plane argument because every case that asks is about a durable
// row: the wisp case's subject is what the release did to the VERSION HISTORY,
// not to the token.
func releaserRowVersion(t *testing.T, ctx context.Context, fixture ReleaserFixture, id string) int64 {
	t.Helper()
	return releaserScalar[int64](t, ctx, fixture,
		"SELECT COALESCE(row_lock, 0) FROM issues WHERE id = ?", id)
}

// releaserUnclaimEvents counts the release entries on one row attributed to one
// actor. It reads BOTH plane tables' answer for the durable one only, because
// every case that uses it seeds a durable row.
func releaserUnclaimEvents(t *testing.T, ctx context.Context, fixture ReleaserFixture, id, actor string) int {
	t.Helper()
	var got int
	err := fixture.QueryScalar(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = 'unclaimed' AND actor = ?",
		[]any{id, actor}, &got)
	if err != nil {
		t.Fatalf("counting unclaimed events for %s by %s: %v", id, actor, err)
	}
	return got
}

// releaserScalar reads one value, failing the test on a query error: every
// caller here is reading a row it has just seeded, so an error is a broken
// fixture rather than an answer.
func releaserScalar[T any](t *testing.T, ctx context.Context, fixture ReleaserFixture, query string, args ...any) T {
	t.Helper()
	var got T
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}

func releaserHistory(t *testing.T, ctx context.Context, fixture ReleaserFixture) int {
	t.Helper()
	entries, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory(): %v", err)
	}
	return entries
}
