//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_claim_lifecycle_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Claimer and Lifecycle contracts, run through client → in-process bd serve
// → reference store.
//
// PARKING. A case that binds a member the v0 wire does not publish is parked
// with skipKnownDivergence naming the refused shape, never by weakening the
// assertion: the case still runs and still passes on every backend that agrees,
// so their behavior stays pinned the day the divergence is found. Everything
// else runs for real.

// skipKnownDivergence is declared untagged in divergence_test.go, and takes the
// ledger row alongside the bead: see its doc for why a park has two coordinates.

// parkBead is the bead every WRITE-side park in this package cites: those
// refused shapes are one decision — the v0 write surface publishes no
// claimNext, no provenance, no plane restriction and no per-edge cycle-check
// flag — and splitting them across beads would suggest they retire
// independently. They do not: they retire when the wire grows the members, or
// not at all.
//
// The population SHRINKS as the wire grows, and twice now it has. The
// precondition members left it with client wave ga-jbuyf, which sent them; the
// two conditional-guard cases that were parked on `patch.assignee` left it with
// ga-7i6by, which carries the patch members. What is left is what the document
// still publishes nothing for.
//
// The read-side parks cite readParkBead instead (served_reader_test.go): they
// are a different decision with a different retirement, and one constant
// covering both would have said otherwise.
const parkBead = "ga-141ra"

func newServedClaimerFixture(t *testing.T, prefix string) conformance.ClaimerFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	claimer, err := env.subject.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	return conformance.ClaimerFixture{
		IssuePrefix: env.prefix,
		Claimer:     claimer,
		CreateIssue: env.createIssue,
		CreateWisp:  env.createWisp,
		// Read from the reference store, like every other hook here: raw rows
		// and workspace config have no wire surface at all, and observing them
		// through the client would be observing the thing under test.
		SetConfig:    env.setConfig,
		QueryScalar:  env.queryScalar,
		CountHistory: env.countHistory,
	}
}

func TestServedClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow(t *testing.T) {
	conformance.RunClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow(t, t.Context(), newServedClaimerFixture(t, "hcl1"))
}

func TestServedClaimerTakesAnOpenIssueTheActorAlreadyHolds(t *testing.T) {
	conformance.RunClaimerTakesAnOpenIssueTheActorAlreadyHolds(t, t.Context(), newServedClaimerFixture(t, "hcl2"))
}

func TestServedClaimerReclaimsItsOwnInProgressIssueWithoutWriting(t *testing.T) {
	conformance.RunClaimerReclaimsItsOwnInProgressIssueWithoutWriting(t, t.Context(), newServedClaimerFixture(t, "hcl3"))
}

func TestServedClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne(t *testing.T) {
	conformance.RunClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne(t, t.Context(), newServedClaimerFixture(t, "hcl4"))
}

func TestServedClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder(t *testing.T) {
	conformance.RunClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder(t, t.Context(), newServedClaimerFixture(t, "hcl5"))
}

func TestServedClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt(t *testing.T) {
	conformance.RunClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt(t, t.Context(), newServedClaimerFixture(t, "hcl6"))
}

func TestServedClaimerRefusesAWispIDAsNotFound(t *testing.T) {
	conformance.RunClaimerRefusesAWispIDAsNotFound(t, t.Context(), newServedClaimerFixture(t, "hcl7"))
}

func TestServedClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState(t *testing.T) {
	conformance.RunClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState(t, t.Context(), newServedClaimerFixture(t, "hcl8"))
}

// TestServedClaimerGrantsALiveLeaseOnTheRowItWonAndLeavesARefusedOneAlone is the
// case with the most to say about this leg specifically, and it runs even though
// the lease vocabulary is on the unsupported allowlist — because the lease is
// not something the CLIENT writes. claimIssue's server-side transaction grants
// it, and the case reads the row back out of band, so what is asserted here is
// that a claim placed over HTTP leaves the same durable lease a local one does.
// A port that answered the row and skipped the grant would ship work no
// heartbeat can extend and no expiry sweep can take back.
func TestServedClaimerGrantsALiveLeaseOnTheRowItWonAndLeavesARefusedOneAlone(t *testing.T) {
	conformance.RunClaimerGrantsALiveLeaseOnTheRowItWonAndLeavesARefusedOneAlone(t, t.Context(), newServedClaimerFixture(t, "hcl9"))
}

// TestServedClaimerReclaimsAcrossSpellingWithoutWriting drives the identity
// sanitization the server applies to the actor. The client sends the dotted
// spelling verbatim — it does not anticipate the rule, for the reason
// L-comment-author records about the author — so this pins that the ROLE behind
// the operation still recognizes the caller as the holder and writes nothing.
func TestServedClaimerReclaimsAcrossSpellingWithoutWriting(t *testing.T) {
	conformance.RunClaimerReclaimsAcrossSpellingWithoutWriting(t, t.Context(), newServedClaimerFixture(t, "hcla"))
}

func TestServedClaimerStampsStartedAtOnceAcrossTheTwoWritesItChoosesBetween(t *testing.T) {
	conformance.RunClaimerStampsStartedAtOnceAcrossTheTwoWritesItChoosesBetween(t, t.Context(), newServedClaimerFixture(t, "hclb"))
}

// TestServedClaimRefusalMessagesCarryTheirFragments is PARKED, and it is the one
// park here that is not about a missing wire member.
//
// The fragments exist so beads.ParseClaimConflict can recover the conflicting
// assignee and status from PROSE. Over the wire that data arrives as typed
// extension members and the reconstructed *ClaimConflictError is whole, so the
// only way to satisfy the message half would be to compose the refusal copy
// client-side — including WHICH copy each refusal shape gets, since an open
// issue held by someone else deliberately omits the assignee tail while an
// in-progress one carries it. That is a second implementation of the producer's
// copy-selection rule, which is the drift the fragments were introduced to
// prevent.
//
// The divergence itself is not left to a skip:
// TestServedClaimConflictCarriesTypedMembersWithoutTheProse below RUNS and
// asserts both halves of it, so ledger row L-claim-prose retires loudly the day
// the wire or the client changes either one.
func TestServedClaimRefusalMessagesCarryTheirFragments(t *testing.T) {
	skipKnownDivergence(t, "L-claim-prose", parkBead,
		"the claim refusal's prose fragments are the server's; over the wire the conflict arrives as typed members and the client does not recompose the copy (asserted by TestServedClaimConflictCarriesTypedMembersWithoutTheProse)")
	conformance.RunClaimerRefusalMessagesCarryTheirFragments(t, t.Context(), newServedClaimerFixture(t, "hclf"))
}

// TestServedClaimConflictCarriesTypedMembersWithoutTheProse is L-claim-prose's
// pin, and it is a RUNNING assertion of the degraded-but-real behavior rather
// than a skip.
//
// Both halves matter and neither is safe alone. The positive half is the reason
// the divergence is acceptable: the conflicting holder and status reach the
// caller TYPED, reconstructed from the problem body's extension members, so
// nothing a caller needs is actually lost. The negative half is the divergence
// itself: the message does not carry the fragments, so a caller reaching for
// beads.ParseClaimConflict gets nothing. If the client ever starts composing
// that copy, or the wire ever stops sending the members, this fails.
func TestServedClaimConflictCarriesTypedMembersWithoutTheProse(t *testing.T) {
	env := newServedEnv(t, "hclp")
	ctx := t.Context()
	claimer, err := env.subject.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}

	const held = "hclp-held"
	seedServedIssue(t, ctx, env, held, types.StatusOpen)
	if _, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "alice", IssueID: held}); err != nil {
		t.Fatalf("first claim of %s: %v", held, err)
	}

	_, err = claimer.Claim(ctx, issueops.ClaimRequest{Actor: "bob", IssueID: held})
	assertTypedClaimConflict(t, err, held, storage.ErrAlreadyClaimed, "alice", types.StatusInProgress)
	assertNoProseFragment(t, err, storage.ErrAlreadyClaimed, storage.ClaimedByFragment)

	const closed = "hclp-closed"
	seedServedIssue(t, ctx, env, closed, types.StatusClosed)
	_, err = claimer.Claim(ctx, issueops.ClaimRequest{Actor: "carol", IssueID: closed})
	assertTypedClaimConflict(t, err, closed, storage.ErrNotClaimable, "", types.StatusClosed)
	assertNoProseFragment(t, err, storage.ErrNotClaimable, storage.NotClaimableStatusFragment)
}

func seedServedIssue(t *testing.T, ctx context.Context, env *servedEnv, id string, status types.Status) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: id, Status: status, Priority: 2, IssueType: types.TypeTask}
	if err := env.createIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func assertTypedClaimConflict(t *testing.T, err error, id string, sentinel error, wantAssignee string, wantStatus types.Status) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("claim of %s = %v, want a refusal wrapping %v", id, err, sentinel)
	}
	var conflict *issueops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("claim of %s is not a *ClaimConflictError (%v); the typed members are the whole reason the prose can be missing", id, err)
	}
	if conflict.IssueID != id {
		t.Errorf("conflict.IssueID = %q, want %q", conflict.IssueID, id)
	}
	if conflict.Assignee != wantAssignee {
		t.Errorf("conflict.Assignee = %q, want %q — reconstructed from the problem body's assignee member", conflict.Assignee, wantAssignee)
	}
	if conflict.Status != wantStatus {
		t.Errorf("conflict.Status = %q, want %q — reconstructed from the problem body's issue_status member", conflict.Status, wantStatus)
	}
}

func assertNoProseFragment(t *testing.T, err error, sentinel error, fragment string) {
	t.Helper()
	marker := sentinel.Error() + fragment
	if strings.Contains(err.Error(), marker) {
		t.Errorf("the refusal message carries %q (%q); L-claim-prose says it does not — if the client now composes the server's copy, retire the row rather than the assertion",
			marker, err.Error())
	}
}

func newServedCloseReopenFixture(t *testing.T, prefix string) conformance.LifecycleCloseReopenFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	return conformance.LifecycleCloseReopenFixture{
		IssuePrefix:   env.prefix,
		Lifecycle:     lifecycle,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		AddDependency: env.addDependency,
		SetConfig:     env.setConfig,
		QueryScalar:   env.queryScalar,
		Exec:          env.exec,
	}
}

func TestServedLifecycleCloseRefusalsCarryTheirTypesAndWriteNothing(t *testing.T) {
	conformance.RunLifecycleCloseRefusalsCarryTheirTypesAndWriteNothing(t, t.Context(), newServedCloseReopenFixture(t, "hlcr"))
}

func TestServedLifecycleCloseAdmitsATransitivelyBlockedTarget(t *testing.T) {
	conformance.RunLifecycleCloseAdmitsATransitivelyBlockedTarget(t, t.Context(), newServedCloseReopenFixture(t, "hlct"))
}

func TestServedLifecycleCloseCountsOpenChildrenInBothPlanes(t *testing.T) {
	conformance.RunLifecycleCloseCountsOpenChildrenInBothPlanes(t, t.Context(), newServedCloseReopenFixture(t, "hlcc"))
}

func TestServedLifecycleCloseAdmitsAStaleBlockFlagWhoseBlockersHaveClosed(t *testing.T) {
	conformance.RunLifecycleCloseAdmitsAStaleBlockFlagWhoseBlockersHaveClosed(t, t.Context(), newServedCloseReopenFixture(t, "hlcs"))
}

func TestServedLifecycleCloseIsIdempotentOnAClosedRowThatStillLooksBlocked(t *testing.T) {
	conformance.RunLifecycleCloseIsIdempotentOnAClosedRowThatStillLooksBlocked(t, t.Context(), newServedCloseReopenFixture(t, "hlcb"))
}

func TestServedLifecycleCloseIsIdempotentAndKeepsTheFirstClose(t *testing.T) {
	conformance.RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose(t, t.Context(), newServedCloseReopenFixture(t, "hlck"))
}

// TestServedLifecycleCloseAndReopenKeepTheClaimHolder is the case the bead names
// by hand: an in-progress row a named actor holds must close, and the holder must
// survive both verbs. It is the claim→close loop an agent actually runs, and the
// one an assignee-clearing close would break silently.
func TestServedLifecycleCloseAndReopenKeepTheClaimHolder(t *testing.T) {
	conformance.RunLifecycleCloseAndReopenKeepTheClaimHolder(t, t.Context(), newServedCloseReopenFixture(t, "hlch"))
}

// The FOUR BLOCKED-STATE cases, which are the close/reopen family's other
// half: every one of them asserts the persisted `is_blocked` projection through
// the shared blocked-state probe, and no read on any role hydrates that column
// — so the flag is a raw-row fact and the probe reads the reference store
// directly. What they prove about THIS leg is that the settlement happens in
// the server's own closing transaction: the client sends one verb and nothing
// client-side recomputes anything, so a projection left stale by the port would
// be visible here and nowhere else on this surface.

func TestServedLifecycleCloseSettlesTheClosedRowItselfAndItsChild(t *testing.T) {
	conformance.RunLifecycleCloseSettlesTheClosedRowItselfAndItsChild(t, t.Context(), newServedCloseReopenFixture(t, "hlbs"))
}

func TestServedLifecycleCloseSettlesItsTransitiveAndCrossPlaneDependers(t *testing.T) {
	conformance.RunLifecycleCloseSettlesItsTransitiveAndCrossPlaneDependers(t, t.Context(), newServedCloseReopenFixture(t, "hlbt"))
}

// TestServedLifecycleCloseOnASpawnersLastChildSatisfiesAWaitsForGate is the
// gate case: closing the last child of a spawner is what SATISFIES a waits-for
// edge, so the depender settles on a close that never named it.
func TestServedLifecycleCloseOnASpawnersLastChildSatisfiesAWaitsForGate(t *testing.T) {
	conformance.RunLifecycleCloseOnASpawnersLastChildSatisfiesAWaitsForGate(t, t.Context(), newServedCloseReopenFixture(t, "hlbg"))
}

func TestServedLifecycleReopenReblocksItsDependers(t *testing.T) {
	conformance.RunLifecycleReopenReblocksItsDependers(t, t.Context(), newServedCloseReopenFixture(t, "hlbr"))
}

func TestServedLifecycleReopenLeavesNonDoneStatusesUnchanged(t *testing.T) {
	conformance.RunLifecycleReopenLeavesNonDoneStatusesUnchanged(t, t.Context(), newServedCloseReopenFixture(t, "hlrn"))
}

func TestServedLifecycleCloseAndReopenSpanTheConfiguredDoneCategory(t *testing.T) {
	conformance.RunLifecycleCloseAndReopenSpanTheConfiguredDoneCategory(t, t.Context(), newServedCloseReopenFixture(t, "hlcd"))
}

// TestServedLifecycleExpectedVersionIsCheckedBeforeTheNoOps was PARKED and now
// RUNS: closeIssue and reopenIssue publish `expected_version` and this client
// sends it, so the ordering the case is really about — the precondition is
// checked BEFORE the idempotent close and before the non-done reopen no-op — is
// asserted end to end rather than described in a ledger row.
func TestServedLifecycleExpectedVersionIsCheckedBeforeTheNoOps(t *testing.T) {
	conformance.RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps(t, t.Context(), newServedCloseReopenFixture(t, "hlev"))
}

// TestServedLifecycleReopenRecordsItsReason runs: `reason` IS on the wire, and
// the case reads it off the reopened event the server records.
func TestServedLifecycleReopenRecordsItsReason(t *testing.T) {
	conformance.RunLifecycleReopenRecordsItsReason(t, t.Context(), newServedCloseReopenFixture(t, "hlrr"))
}

func TestServedLifecycleResultsAreHydratedPostStateSnapshots(t *testing.T) {
	conformance.RunLifecycleResultsAreHydratedPostStateSnapshots(t, t.Context(), newServedCloseReopenFixture(t, "hlrh"))
}

// TestServedLifecycleResultsCarryThePostWriteRowVersion is the leg the whole slice
// is for: updateIssue, closeIssue and reopenIssue publish the post-write token as
// a sibling `revision` member (types.Issue.RowVersion is `json:"-"`), and this
// client stitches it back onto Issue.RowVersion. A port that dropped the member
// would answer a zero here and pass no other case on this surface, and the case's
// guarded update→close chain is gc's own — the token the update answers with is
// fed straight into the close's ExpectedVersion with no intervening Get.
func TestServedLifecycleResultsCarryThePostWriteRowVersion(t *testing.T) {
	conformance.RunLifecycleResultsCarryThePostWriteRowVersion(t, t.Context(), newServedCloseReopenFixture(t, "hlrv"))
}

func TestServedLifecycleCloseAndReopenRequireActorAndIssueID(t *testing.T) {
	conformance.RunLifecycleCloseAndReopenRequireActorAndIssueID(t, t.Context(), newServedCloseReopenFixture(t, "hlra"))
}

// TestServedLifecycleReopenProvenanceLabelsHistory is PARKED: reopenIssue
// publishes no provenance member and the server writes its own fixed label, so
// the client refuses the field rather than letting a caller believe their label
// reached the history entry.
func TestServedLifecycleReopenProvenanceLabelsHistory(t *testing.T) {
	skipKnownDivergence(t, "W-ReopenRequest.Provenance", parkBead,
		"reopenIssue publishes no provenance member; the client refuses it and the server writes its own label")
	conformance.RunLifecycleReopenProvenanceLabelsHistory(t, t.Context(), newServedCloseReopenFixture(t, "hlrp"))
}

func newServedUpdateFixture(t *testing.T, prefix string) conformance.LifecycleUpdateFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	return conformance.LifecycleUpdateFixture{
		IssuePrefix:   env.prefix,
		Lifecycle:     lifecycle,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		GetIssue:      env.getIssue,
		AddDependency: env.addDependency,
		// The workspace vocabulary the transfer fence's one configured term
		// (claim.pools) is read against. The case skipped without it while it
		// was parked, and would have kept skipping silently once it was not.
		SetConfig: env.setConfig,
		// The two out-of-band reads the patch-member wave's cases need, both
		// bound to the reference store like every other seed hook here:
		// ListEvents ends the "the refusal wrote nothing" clause a row read
		// cannot, and ListDependencies answers the parent SET a reparent
		// replaces.
		ListEvents:       env.listEvents,
		ListDependencies: env.listDependencies,
	}
}

func TestServedLifecycleUpdatePersistsThePatchAndHydratesTheResult(t *testing.T) {
	conformance.RunLifecycleUpdatePersistsThePatchAndHydratesTheResult(t, t.Context(), newServedUpdateFixture(t, "hlup"))
}

func TestServedLifecycleUpdateReportsNoChangeForASameValuePatch(t *testing.T) {
	conformance.RunLifecycleUpdateReportsNoChangeForASameValuePatch(t, t.Context(), newServedUpdateFixture(t, "hlus"))
}

func TestServedLifecycleUpdateAppendsNotesWithoutReplacingThem(t *testing.T) {
	conformance.RunLifecycleUpdateAppendsNotesWithoutReplacingThem(t, t.Context(), newServedUpdateFixture(t, "hlun"))
}

// TestServedLifecycleUpdateClearsTheNullableMembers is the case that proves the
// patch document carries an explicit null rather than omitting the member.
func TestServedLifecycleUpdateClearsTheNullableMembers(t *testing.T) {
	conformance.RunLifecycleUpdateClearsTheNullableMembers(t, t.Context(), newServedUpdateFixture(t, "hluc"))
}

func TestServedLifecycleUpdateReplacesTheLabelSet(t *testing.T) {
	conformance.RunLifecycleUpdateReplacesTheLabelSet(t, t.Context(), newServedUpdateFixture(t, "hlul"))
}

// TestServedLifecycleUpdateResolvesBothPlanesUnlessRestricted is PARKED: half
// the case sets IssuePlaneOnly, which updateIssue publishes no member for. The
// client refuses it because dropping it would EDIT the wisp the caller asked to
// be told did not exist.
func TestServedLifecycleUpdateResolvesBothPlanesUnlessRestricted(t *testing.T) {
	skipKnownDivergence(t, "W-UpdateRequest.IssuePlaneOnly", parkBead,
		"updateIssue publishes no plane restriction; the client refuses IssuePlaneOnly rather than silently editing the wisp plane")
	conformance.RunLifecycleUpdateResolvesBothPlanesUnlessRestricted(t, t.Context(), newServedUpdateFixture(t, "hlrb"))
}

// TestServedLifecycleUpdatePreservesTheCreationStamp runs: nothing about it
// touches a refused member. It is worth having over this wire rather than only
// locally because the stamp is the one pair of columns createIssue deliberately
// will not let a caller write (W-CreateRequest.Issue), so a server that rewrote
// it on an ORDINARY EDIT would put the row out of step with the journal entry
// that recorded its creation, with no request member anywhere to blame.
func TestServedLifecycleUpdatePreservesTheCreationStamp(t *testing.T) {
	conformance.RunLifecycleUpdatePreservesTheCreationStamp(t, t.Context(), newServedUpdateFixture(t, "hlcs"))
}

// Two of the four ExpectedVersion concurrency races, run for real over this
// wire: N goroutines dial N concurrent HTTP requests against the same
// in-process server, racing the same --if-revision token.
// precondition_failed decodes to storage.ErrVersionMismatch
// (internal/httpclient/wire/problem.go), the same sentinel
// storage.ErrVersionMismatch aliases to (internal/storage/storage.go), so
// errors.Is resolves identically whether the loser learned it locally or over
// http.
//
// The other two of the four race IssuePatch.SpecID/.AwaitID/.Owner updates
// against each other (RunLifecycleUpdateExpectedVersionSingleWinnerWithDisjointColumnsUnderConcurrency,
// RunLifecycleUpdateExpectedVersionSingleWinnerAcrossUpdateAndCloseUnderConcurrency),
// and stay off this leg: those three fields are refused on updateIssue by
// name (W-IssuePatch.SpecID, W-IssuePatch.AwaitID, W-IssuePatch.Owner,
// internal/httpclient/encode/ledger.go), a pre-existing D8 refuse-not-drop
// decision this slice does not reopen, not a race outcome the wire could
// report correctly — see issuePatchFieldsNotOnUpdateWireWaiverReason
// (internal/storage/leg_contract_wiring_test.go).

func TestServedLifecycleUpdateExpectedVersionSingleWinnerUnderConcurrency(t *testing.T) {
	conformance.RunLifecycleUpdateExpectedVersionSingleWinnerUnderConcurrency(t, t.Context(), newServedUpdateFixture(t, "hlvu"))
}

func TestServedLifecycleCloseExpectedVersionSingleWinnerUnderConcurrency(t *testing.T) {
	conformance.RunLifecycleCloseExpectedVersionSingleWinnerUnderConcurrency(t, t.Context(), newServedUpdateFixture(t, "hlvc"))
}

// The THREE claim-and-override cases below RUN, and left the park population
// together, with the #7247 review port: `claim`, `force_assignee_transfer` and
// `force_close_policy` are top-level members of updateIssue's body (the first
// since upstream #6890), and the client sends each exactly as it sent
// `force_notes_overwrite` before them. Their rows (W-UpdateRequest.Claim,
// .ForceAssigneeTransfer, .ForceClosePolicy) are RETIRED.
//
// What still parks after them is TWO members updateIssue publishes nothing for
// (W-IssuePatch.Persistence, W-UpdateRequest.Provenance), each named so the
// lock counts the contract rather than reading it as unwritten. Both refusals
// are themselves asserted by TestUpdateRefusesEveryMemberTheWireExcludes, which
// RUNS, so no park here stands in for an unpinned behavior.

// TestServedLifecycleUpdateClaimIsAMutationWhenThePatchRestoresTheRow is the
// claim folded into an update: a mutation even when the patch beside it restores
// the row. That fold is the server's own since #6890 — claimed and patched in
// one transaction by the role the direct route runs — so the case is one
// updateIssue call here, as it is one Lifecycle.Update call locally.
func TestServedLifecycleUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t *testing.T) {
	conformance.RunLifecycleUpdateClaimIsAMutationWhenThePatchRestoresTheRow(t, t.Context(), newServedUpdateFixture(t, "hlcm"))
}

// predatesUpdateClaimTransport plays a bd serve from before upstream #6890, as
// far as updateIssue's `claim` member goes. Every request reaches the
// in-process server untouched EXCEPT an updateIssue body carrying `claim`,
// which it answers the way such a server did: the skew 400 naming the member,
// raised before any database work. Everything else about that server —
// claimIssue among it — is the current one's, which is the point: the
// fallback route it forces is claimIssue, served for real.
type predatesUpdateClaimTransport struct {
	next http.RoundTripper
	// refused counts the claims it answered, so a case can tell the fallback
	// ran from a current-server claim that happened to agree with it.
	refused atomic.Int32
}

func (p *predatesUpdateClaimTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPatch && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		var members map[string]json.RawMessage
		decodeErr := json.NewDecoder(body).Decode(&members)
		_ = body.Close()
		if _, claims := members["claim"]; decodeErr == nil && claims {
			p.refused.Add(1)
			if req.Body != nil {
				_ = req.Body.Close()
			}
			return &http.Response{
				Status:     "400 Bad Request",
				StatusCode: http.StatusBadRequest,
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
				Header:     http.Header{"Content-Type": {"application/problem+json"}},
				Body: io.NopCloser(strings.NewReader(`{"type":"about:blank","title":"Bad Request","status":400,` +
					`"code":"invalid_argument","reason":"unknown_parameter","param":"claim",` +
					`"detail":"unknown request body member \"claim\"","request_id":"predates-6890"}`)),
				ContentLength: -1,
				Request:       req,
			}, nil
		}
	}
	return p.next.RoundTrip(req)
}

// predatingUpdateClaim is a second client store over env's server, dialing it
// through predatesUpdateClaimTransport.
func (env *servedEnv) predatingUpdateClaim(t *testing.T) (*Store, *predatesUpdateClaimTransport) {
	t.Helper()
	transport := &predatesUpdateClaimTransport{next: http.DefaultTransport}
	client, err := wire.New(env.subject.target.BaseURL, nil, wire.Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("build the predating client: %v", err)
	}
	return New(env.subject.target, client, nil), transport
}

// TestServedUpdateClaimsAWisp pins the direct route's wisp claim over this
// wire: `bd update <id> --claim` on a wisp claims it, because updateIssue's
// claim resolves both planes like the rest of the operation (it sets no
// IssuePlaneOnly), exactly as the local Lifecycle.Update does.
//
// It used to refuse by name on every server (W-ClaimRequest.Wisp), when the
// claim went through claimIssue, whose role excludes the wisp plane. That
// refusal survives only on the fallback route — see the case below.
func TestServedUpdateClaimsAWisp(t *testing.T) {
	env := newServedEnv(t, "hlcx")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const id = "hlcx-wisp"
	if err := env.createWisp(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seed the wisp %s: %v", id, err)
	}

	result, err := lifecycle.Update(ctx, issueops.UpdateRequest{Actor: "claimant", IssueID: id, Claim: true})
	if err != nil {
		t.Fatalf("claim-only update on a wisp: %v", err)
	}
	if !result.Changed || result.Issue == nil ||
		result.Issue.Status != types.StatusInProgress || result.Issue.Assignee != "claimant" {
		t.Fatalf("claim-only update on a wisp answered %+v, want the claimed row", result)
	}
	// The reference store's GetIssue auto-routes to the wisps table when the
	// durable one has no such row (issueops.GetIssueInTx), so this reads the
	// wisp back the same way env.getIssue would for a durable row.
	row, err := env.reference.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("read the wisp back: %v", err)
	}
	if row.Status != types.StatusInProgress || row.Assignee != "claimant" {
		t.Errorf("the claim left %s as %s/%q, want %s/%q", id, row.Status, row.Assignee, types.StatusInProgress, "claimant")
	}
	if !isWispIssue(row) {
		t.Errorf("the claim moved %s off the wisp plane", id)
	}
}

// TestClaimOnlyUpdateRefusesAWispByNameOnAServerThatPredatesUpdateClaim is
// W-ClaimRequest.Wisp's pin.
//
// Against a server that predates `claim` on updateIssue, a claim-only
// UpdateRequest falls back to claimIssue (httpLifecycle.claimOnlyUpdate), and
// claimIssue's role excludes the wisp plane on every backend (see
// TestServedClaimerRefusesAWispIDAsNotFound next door). Left alone, a wisp id
// would surface as the wire's generic not-found — "no issue or wisp with that
// id" — a live row reported as though it does not exist. This case pins that
// the fallback instead recognizes the id names a wisp and refuses BY NAME,
// leaving the wisp row untouched.
func TestClaimOnlyUpdateRefusesAWispByNameOnAServerThatPredatesUpdateClaim(t *testing.T) {
	env := newServedEnv(t, "hlcw")
	ctx := t.Context()
	predating, transport := env.predatingUpdateClaim(t)
	lifecycle, err := predating.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const id = "hlcw-wisp"
	if err := env.createWisp(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seed the wisp %s: %v", id, err)
	}

	_, err = lifecycle.Update(ctx, issueops.UpdateRequest{Actor: "claimant", IssueID: id, Claim: true})
	if !errors.Is(err, encode.ErrRefused) {
		t.Fatalf("claim-only update on a wisp id returned %v, want a named refusal", err)
	}
	var refusal *encode.RefusedError
	if errors.As(err, &refusal) && refusal.Row.ID != "W-ClaimRequest.Wisp" {
		t.Errorf("the refusal cites %q, want W-ClaimRequest.Wisp", refusal.Row.ID)
	}
	if got := transport.refused.Load(); got != 1 {
		t.Errorf("the predating server refused %d claims, want 1: the case must reach the fallback through the skew refusal", got)
	}

	// The refusal left the wisp exactly as seeded: still open, still
	// unassigned. A wisp claim that fell through to the misleading not-found
	// would still leave the row untouched, so this is not the case's whole
	// subject — but a refusal-by-name that quietly claimed anyway would be a
	// worse bug than the one it replaces.
	row, err := env.reference.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("read the wisp back: %v", err)
	}
	if row.Status != types.StatusOpen || row.Assignee != "" {
		t.Errorf("the refused claim left %s as %s/%q, want open/unassigned", id, row.Status, row.Assignee)
	}
}

// TestClaimOnlyUpdateHydratesLabelsAndCreatedByForOutputParity pins
// `bd update <id> --claim --json` printing the same "labels" and "created_by"
// over an http workspace as the direct route does, on BOTH routes a claim-only
// update can take.
//
// On a current server the claim is updateIssue's, which answers the hydrated
// row and the post-write `revision` like every other update, so parity is the
// operation's own. On a server that predates the member, the claim falls back
// to claimIssue, whose role answers the BARE row — issueops.Claimer's own
// contract on every backend, pinned against httpClaimer.Claim directly by
// TestServedClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow — so the
// fallback (claimOnlyUpdate, lifecycle.go) reads the labels and CreatedBy back
// itself. What it does NOT read back is the row version: claimIssue carries no
// `revision`, and the follow-up read's token is another snapshot's.
func TestClaimOnlyUpdateHydratesLabelsAndCreatedByForOutputParity(t *testing.T) {
	env := newServedEnv(t, "hlch")
	ctx := t.Context()
	predating, transport := env.predatingUpdateClaim(t)

	for _, route := range []struct {
		name  string
		store *Store
		// wantRevision says whether the claimed row carries the post-claim
		// row version, which only updateIssue answers with.
		wantRevision bool
		wantRefused  int32
	}{
		{name: "current server", store: env.subject, wantRevision: true, wantRefused: 0},
		{name: "server predating update claim", store: predating, wantRevision: false, wantRefused: 1},
	} {
		t.Run(route.name, func(t *testing.T) {
			lifecycle, err := route.store.IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			id := "hlch-" + strings.ReplaceAll(route.name, " ", "-")
			if err := env.createIssue(ctx, &types.Issue{
				ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
				IssueType: types.TypeTask, Labels: []string{"hlch-label"},
				CreatedBy: "the-original-author",
			}, "the-original-author"); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
			before := transport.refused.Load()

			result, err := lifecycle.Update(ctx, issueops.UpdateRequest{Actor: "claimant", IssueID: id, Claim: true})
			if err != nil {
				t.Fatalf("claim-only update on a labeled issue: %v", err)
			}
			if got := transport.refused.Load() - before; got != route.wantRefused {
				t.Fatalf("the predating server refused %d claims, want %d: the route under test is not the one that ran", got, route.wantRefused)
			}
			if result.Issue == nil {
				t.Fatal("claim-only update returned a nil Issue")
			}
			if !slices.Contains(result.Issue.Labels, "hlch-label") {
				t.Errorf("claimed Issue.Labels = %v, want [%q] — the direct route's `bd update --claim` always hydrates labels, and this is what makes the http route's --json output match it", result.Issue.Labels, "hlch-label")
			}
			if result.Issue.CreatedBy != "the-original-author" {
				t.Errorf("claimed Issue.CreatedBy = %q, want %q", result.Issue.CreatedBy, "the-original-author")
			}
			// The claim itself still won: hydration is an enrichment, not a
			// substitute for the actual mutation.
			if result.Issue.Status != types.StatusInProgress || result.Issue.Assignee != "claimant" {
				t.Errorf("claimed issue = %s/%q, want %s/%q", result.Issue.Status, result.Issue.Assignee, types.StatusInProgress, "claimant")
			}

			row, err := env.getIssue(ctx, id)
			if err != nil {
				t.Fatalf("read back %s: %v", id, err)
			}
			switch {
			case route.wantRevision && (row.RowVersion == 0 || result.Issue.RowVersion != row.RowVersion):
				t.Errorf("claimed Issue.RowVersion = %d, want the post-claim row's %d — a guarded write composes its next ExpectedVersion from it", result.Issue.RowVersion, row.RowVersion)
			case !route.wantRevision && result.Issue.RowVersion != 0:
				t.Errorf("the fallback's Issue.RowVersion = %d, want it unset: claimIssue answers no revision, and no other snapshot's token may stand in for one", result.Issue.RowVersion)
			}
		})
	}
}

// TestServedLifecycleUpdateAssigneeTransferFence runs the fence AND its
// override: a transfer away from a live foreign in-progress holder refuses with
// already_claimed, the ExpectedAssignee bypass is sent, and so — since the
// #7247 review port — is `force_assignee_transfer`, the unconditional override
// the case ends on. TestServedUpdateGuardTrioGatesTheEdit is where the guard
// half runs on its own.
func TestServedLifecycleUpdateAssigneeTransferFence(t *testing.T) {
	conformance.RunLifecycleUpdateAssigneeTransferFence(t, t.Context(), newServedUpdateFixture(t, "hlxf"))
}

// TestServedLifecycleUpdateClosePolicy runs both halves of the policy: a status
// crossing into the done category with an open child or a live blocker refuses,
// typed, and writes nothing; and `force_close_policy`, which the case's second
// half forces the crossing with, is sent since the #7247 review port, beside
// the claim its compound arm drives.
func TestServedLifecycleUpdateClosePolicy(t *testing.T) {
	conformance.RunLifecycleUpdateClosePolicy(t, t.Context(), newServedUpdateFixture(t, "hlcp"))
}

// TestServedLifecycleUpdatePersistentPreservesUnversionedClass parks on
// IssuePatch.Persistence, which neither IssuePatchBody nor ApplyPatchBody
// publishes: moving a row between planes mid-plan is a different act from
// writing its fields, and a dropped persistence mode would answer "no change"
// to a request that asked for one.
func TestServedLifecycleUpdatePersistentPreservesUnversionedClass(t *testing.T) {
	skipKnownDivergence(t, "W-IssuePatch.Persistence", parkBead,
		"the case restates an unversioned row as persistent, and the wire publishes no persistence member "+
			"on either patch body; the client refuses it rather than reporting a no-op it did not ask the server for")
	conformance.RunLifecycleUpdatePersistentPreservesUnversionedClass(t, t.Context(), newServedUpdateFixture(t, "hlpu"))
}

// TestServedLifecycleUpdateProvenanceLabelsHistory is the update-side twin of
// the reopen park above: the whole purpose of the field is the LABEL on the
// history entry, and the server writes its own, so dropping it would leave the
// entry naming the server's surface while the caller believed it named theirs.
func TestServedLifecycleUpdateProvenanceLabelsHistory(t *testing.T) {
	skipKnownDivergence(t, "W-UpdateRequest.Provenance", parkBead,
		"updateIssue publishes no provenance member and the server writes its own label; the field's whole "+
			"purpose is that label, so refusing it is refusing the request rather than narrowing it")
	conformance.RunLifecycleUpdateProvenanceLabelsHistory(t, t.Context(), newServedUpdateFixture(t, "hlpv"))
}

// TestServedLifecycleUpdateRefusesATemplate is the served leg of the template
// guard: bd serve refuses with template_read_only and the client rebuilds the
// typed error — sentence included — the embedded store returns.
func TestServedLifecycleUpdateRefusesATemplate(t *testing.T) {
	conformance.RunLifecycleUpdateRefusesATemplate(t, t.Context(), newServedUpdateFixture(t, "hlut"))
}

// TestServedLifecycleUpdateAllowTemplateEditsATemplate is the served leg of
// the guard's stand-down: allow_template rides the wire and the role honours it.
func TestServedLifecycleUpdateAllowTemplateEditsATemplate(t *testing.T) {
	conformance.RunLifecycleUpdateAllowTemplateEditsATemplate(t, t.Context(), newServedUpdateFixture(t, "hlua"))
}

func TestServedLifecycleUpdateRefusesUnknownIDsAndActorlessRequests(t *testing.T) {
	conformance.RunLifecycleUpdateRefusesUnknownIDsAndActorlessRequests(t, t.Context(), newServedUpdateFixture(t, "hluu"))
}

func TestServedLifecycleUpdateRefusalWritesNoMemberOfThePatch(t *testing.T) {
	conformance.RunLifecycleUpdateRefusalWritesNoMemberOfThePatch(t, t.Context(), newServedUpdateFixture(t, "hlur"))
}

// The two CONDITIONAL-GUARD contracts RUN, and their park is the one client
// wave ga-7i6by retired: both assign through `patch.assignee` partway in, which
// the client refused until that wave carried the member. Nothing about the
// guards themselves changed — they were sent from wave ga-jbuyf — so what these
// two add over the bespoke pin below is the state the parks could not reach
// without seeding it out of band.

func TestServedLifecycleUpdateConditionalGuardsGateOrdinaryEdits(t *testing.T) {
	conformance.RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits(t, t.Context(), newServedUpdateFixture(t, "hlcg"))
}

func TestServedLifecycleUpdateConditionalGuardAcceptsRespelledAssignee(t *testing.T) {
	conformance.RunLifecycleUpdateConditionalGuardAcceptsRespelledAssignee(t, t.Context(), newServedUpdateFixture(t, "hlcq"))
}

// TestServedLifecycleUpdateMetadataPatchOrdersMergeSetUnset is the metadata
// member's own contract, and the case that makes the algebra falsifiable: every
// key in it collides across merge, set and unset, so a client that reordered the
// arms — or dropped one — produces a different document rather than a plausible
// one. Its second half drives the replace-plus-incremental contradiction, which
// both the wire and the role refuse as a validation failure writing nothing.
func TestServedLifecycleUpdateMetadataPatchOrdersMergeSetUnset(t *testing.T) {
	conformance.RunLifecycleUpdateMetadataPatchOrdersMergeSetUnset(t, t.Context(), newServedUpdateFixture(t, "hlmo"))
}

// The two REPARENT contracts, which are the reason `parent_id` is a member
// rather than a pair of dependency edits: it replaces every parent atomically,
// and the two-call spelling leaves the issue parentless if the second call
// fails.
func TestServedLifecycleUpdateParentIDReplacesTheParentEdge(t *testing.T) {
	conformance.RunLifecycleUpdateParentIDReplacesTheParentEdge(t, t.Context(), newServedUpdateFixture(t, "hlpe"))
}

func TestServedLifecycleUpdateParentIDReplacesEveryParent(t *testing.T) {
	conformance.RunLifecycleUpdateParentIDReplacesEveryParent(t, t.Context(), newServedUpdateFixture(t, "hlpa"))
}

// TestServedUpdateGuardTrioGatesTheEdit is the guard trio's end-to-end pin. It
// was written because the two contracts above were parked on a PATCH member
// rather than on a precondition; it stays now that they run, because it drives
// each guard in BOTH polarities against a row whose holder was seeded out of
// band, which is the arm a case that patches its way into the state cannot have.
//
// Every arm asserts both halves: a satisfied guard applies the edit, and a stale
// one refuses with the sentinel the embedded store returns for the same miss and
// writes NOTHING.
func TestServedUpdateGuardTrioGatesTheEdit(t *testing.T) {
	env := newServedEnv(t, "hlgt")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const id = "hlgt-guarded"
	if err := env.createIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask, Assignee: "holder",
	}, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}

	edit := func(priority int) issueops.UpdateRequest {
		return issueops.UpdateRequest{
			Actor: "writer", IssueID: id,
			Patch: issueops.IssuePatch{Priority: issueops.Field[int]{Set: true, Value: priority}},
		}
	}
	priority := func() int {
		t.Helper()
		row, err := env.getIssue(ctx, id)
		if err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		return row.Priority
	}
	version := func() int64 {
		t.Helper()
		row, err := env.getIssue(ctx, id)
		if err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		if row.RowVersion == 0 {
			t.Fatalf("%s carries RowVersion 0, so no version guard below could fail: the token is the subject", id)
		}
		return row.RowVersion
	}

	// STATUS, both polarities. It is the readable guard — every read of this
	// surface carries the status — so a caller can guard a transition with no
	// token at all.
	open, closed := issueops.StatusOpen, issueops.StatusClosed
	satisfied := edit(1)
	satisfied.ExpectedStatus = &open
	if res, err := lifecycle.Update(ctx, satisfied); err != nil || !res.Changed {
		t.Fatalf("edit guarded on the current status = %#v, %v; want it applied", res, err)
	}
	if got := priority(); got != 1 {
		t.Fatalf("priority after a satisfied status guard = %d, want 1", got)
	}

	stale := edit(4)
	stale.ExpectedStatus = &closed
	if _, err := lifecycle.Update(ctx, stale); !errors.Is(err, issueops.ErrStatusMismatch) {
		t.Fatalf("edit guarded on a stale status: err = %v, want ErrStatusMismatch", err)
	}
	if got := priority(); got != 1 {
		t.Errorf("priority after a stale status guard = %d, want 1 — the refusal wrote nothing", got)
	}

	// ASSIGNEE, both polarities, including the EMPTY guard that says "only if
	// nobody holds it" — which is stale here, because the seed named a holder.
	holder, unassigned := "holder", ""
	onHolder := edit(2)
	onHolder.ExpectedAssignee = &holder
	if res, err := lifecycle.Update(ctx, onHolder); err != nil || !res.Changed {
		t.Fatalf("edit guarded on the current holder = %#v, %v; want it applied", res, err)
	}
	if got := priority(); got != 2 {
		t.Fatalf("priority after a satisfied assignee guard = %d, want 2", got)
	}

	onNobody := edit(4)
	onNobody.ExpectedAssignee = &unassigned
	if _, err := lifecycle.Update(ctx, onNobody); !errors.Is(err, issueops.ErrAssigneeMismatch) {
		t.Fatalf("edit guarded on unassigned against a held row: err = %v, want ErrAssigneeMismatch", err)
	}
	if got := priority(); got != 2 {
		t.Errorf("priority after a stale assignee guard = %d, want 2 — the refusal wrote nothing", got)
	}

	// VERSION, both polarities. The satisfied leg hands back the row's own token,
	// which is the compare-and-set a caller that already read the row performs;
	// the stale leg is a token no writer could have produced.
	current := version()
	onVersion := edit(3)
	onVersion.ExpectedVersion = &current
	if res, err := lifecycle.Update(ctx, onVersion); err != nil || !res.Changed {
		t.Fatalf("edit guarded on the row's current version = %#v, %v; want it applied", res, err)
	}
	if got := priority(); got != 3 {
		t.Fatalf("priority after a satisfied version guard = %d, want 3", got)
	}

	after := version()
	staleVersion := after + 1
	onStale := edit(4)
	onStale.ExpectedVersion = &staleVersion
	if _, err := lifecycle.Update(ctx, onStale); !errors.Is(err, issueops.ErrVersionMismatch) {
		t.Fatalf("edit guarded on a stale version: err = %v, want ErrVersionMismatch", err)
	}
	if got := priority(); got != 3 {
		t.Errorf("priority after a stale version guard = %d, want 3 — the refusal wrote nothing", got)
	}
	// The refusal wrote nothing AT ALL, the token included: a guard that
	// reminted row_lock on its way to refusing would make the caller's next
	// read-and-retry lose a second time for a reason it could never see.
	if got := version(); got != after {
		t.Errorf("row version after a refused guarded update = %d, want %d unchanged", got, after)
	}

	// ALL THREE AT ONCE, with the LAST one stale. Every refusal above puts the
	// stale guard alone, so a body that stopped at the first present
	// precondition would answer all of them correctly and let this one through.
	together := edit(4)
	together.ExpectedVersion = &after
	together.ExpectedAssignee = &holder
	together.ExpectedStatus = &closed
	if _, err := lifecycle.Update(ctx, together); !errors.Is(err, issueops.ErrStatusMismatch) {
		t.Fatalf("edit with two holding guards and a stale status: err = %v, want ErrStatusMismatch", err)
	}
	if got := priority(); got != 3 {
		t.Errorf("priority after a stale guard behind two holding ones = %d, want 3", got)
	}
}

// TestServedUpdateUnassignsThroughTheEmptyAssignee is the empty string's own
// end-to-end proof, and it exists because nothing else was one.
//
// The client unit pins that "" reaches the patch document and the role contract
// pins that an unassign clears the holder, but until this ran NO case drove
// Assignee{Set: true, Value: ""} client → bd serve → role and read the row back:
// the two halves were proved on opposite sides of a wire that has to carry a
// PRESENT EMPTY member for either to matter. `parent_id`, whose empty string
// means the same kind of thing, got both polarities served through its own
// contracts; this member had no contract to inherit one from, because the role
// tier reaches an unassign only through cases that seed with Claim.
//
// THE SECOND ARM IS THE FALSIFIABLE ONE. A client that skipped the member
// because its value was empty fails the first arm LOUDLY — the document would
// be empty and the request would never dial — but that is not the shape the
// silent-no-edit trap takes. The trap is an unassign travelling BESIDE another
// edit: the other edit lands, the server answers 200, and only a read of the
// assignee can tell that the part the caller cared about was dropped.
func TestServedUpdateUnassignsThroughTheEmptyAssignee(t *testing.T) {
	env := newServedEnv(t, "hlua")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	seed := func(id, holder string) {
		t.Helper()
		if err := env.createIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Assignee: holder,
		}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	assignee := func(id string) string {
		t.Helper()
		row, err := env.getIssue(ctx, id)
		if err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		return row.Assignee
	}

	t.Run("alone", func(t *testing.T) {
		const id = "hlua-alone"
		seed(id, "holder")

		unassign := issueops.UpdateRequest{
			Actor: "writer", IssueID: id,
			Patch: issueops.IssuePatch{Assignee: issueops.Field[string]{Set: true}},
		}
		res, err := lifecycle.Update(ctx, unassign)
		if err != nil {
			t.Fatalf("unassign %s: %v", id, err)
		}
		if !res.Changed {
			t.Error("the unassign reported Changed = false; it moved the holder off the row")
		}
		if got := assignee(id); got != "" {
			t.Fatalf("%s is still held by %q; the empty assignee did not reach the row", id, got)
		}
		// The result is the post-state snapshot, so it has to agree with the
		// row rather than echo the request.
		if res.Issue == nil || res.Issue.Assignee != "" {
			t.Errorf("the result still reports an assignee: %#v", res.Issue)
		}

		// A SECOND unassign is a same-value patch, not a second edit: the
		// server answers 200 with changed:false and touches nothing, which is
		// the idempotence every other member on this surface promises.
		repeat, err := lifecycle.Update(ctx, unassign)
		if err != nil {
			t.Fatalf("re-unassign %s: %v", id, err)
		}
		if repeat.Changed {
			t.Error("re-unassigning an unheld row reported Changed = true; it restated the current value")
		}
		if got := assignee(id); got != "" {
			t.Errorf("%s picked an assignee back up: %q", id, got)
		}
	})

	t.Run("beside another edit", func(t *testing.T) {
		const id = "hlua-beside"
		seed(id, "holder")

		res, err := lifecycle.Update(ctx, issueops.UpdateRequest{
			Actor: "writer", IssueID: id,
			Patch: issueops.IssuePatch{
				Assignee: issueops.Field[string]{Set: true},
				Priority: issueops.Field[int]{Set: true, Value: 0},
			},
		})
		if err != nil {
			t.Fatalf("unassign %s beside a priority edit: %v", id, err)
		}
		if !res.Changed {
			t.Error("the combined edit reported Changed = false")
		}
		row, err := env.getIssue(ctx, id)
		if err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		// The priority is the DECOY: it lands whether or not the assignee did,
		// so a dropped unassign leaves a request that looks entirely successful.
		if row.Priority != 0 {
			t.Errorf("%s priority = %d, want 0", id, row.Priority)
		}
		if row.Assignee != "" {
			t.Fatalf("%s is still held by %q while the priority edit beside it landed — "+
				"this is the silent no-edit the empty string exists to prevent", id, row.Assignee)
		}
	})
}

// TestServedUpdateMetadataClearAgreesWithALocalClear is the CLEAR's end-to-end
// proof, and it is a DIFFERENTIAL rather than an assertion about bytes.
//
// The clear is the one state the wire's own struct cannot spell — `omitempty`
// omits a set-but-empty replacement — so the client substitutes the empty
// document, which is what the role's own ApplyMetadataPatch substitutes before
// it writes. Both substitutions were verified by reading the two sources; what
// no case did was run the request through the wire and compare the row it left
// against the row the SAME request leaves locally. A substitution that was wrong
// in the same way on both sides would satisfy a golden value and fail this.
func TestServedUpdateMetadataClearAgreesWithALocalClear(t *testing.T) {
	env := newServedEnv(t, "hlmc")
	ctx := t.Context()
	remote, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	// The reference store's OWN role, which is what a local workspace runs. It
	// is the control arm, so it is deliberately not the subject.
	local, err := env.reference.IssueLifecycle()
	if err != nil {
		t.Fatalf("reference IssueLifecycle(): %v", err)
	}

	const seeded = `{"gc.lease":"win44","phase":"held"}`
	clear := func(id string, through issueops.Lifecycle) *types.Issue {
		t.Helper()
		if err := env.createIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Metadata: json.RawMessage(seeded),
		}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		res, err := through.Update(ctx, issueops.UpdateRequest{
			Actor: "writer", IssueID: id,
			Patch: issueops.IssuePatch{Metadata: issueops.MetadataPatch{
				// SET with no bytes: the clear as the role spells it.
				Replace: issueops.Field[json.RawMessage]{Set: true},
			}},
		})
		if err != nil {
			t.Fatalf("clear the metadata of %s: %v", id, err)
		}
		if !res.Changed {
			t.Errorf("clearing %s reported Changed = false; the row held %s", id, seeded)
		}
		row, err := env.getIssue(ctx, id)
		if err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		return row
	}

	overWire := clear("hlmc-remote", remote)
	locally := clear("hlmc-local", local)

	// Compared as DOCUMENTS, because absent, empty and `{}` are one value on
	// the way out and the role says so — a byte comparison would fail on a
	// difference no reader of this plane can observe.
	document := func(label string, row *types.Issue) any {
		t.Helper()
		raw := row.Metadata
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s metadata %s: %v", label, raw, err)
		}
		return out
	}
	got, want := document("cleared over the wire", overWire), document("cleared locally", locally)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a CLEAR through the client left %s; the same request run locally left %s",
			overWire.Metadata, locally.Metadata)
	}
	// And the clear really cleared: a differential alone would pass on two
	// backends that both did nothing.
	if !reflect.DeepEqual(got, map[string]any{}) {
		t.Errorf("the cleared row still carries %s", overWire.Metadata)
	}
}

// TestServedUpdateAppliesTheOrderedLabelEditServerSide is the incremental label
// edit end to end, and it is a bespoke case because no contract covers it here.
//
// The two role contracts that pin the algebra — RunIssueOperationsUpdateLabelPatchOrdering
// and its value-rules sibling — belong to the IssueOperations family, which this
// leg does not wire at all, so wiring add_labels and remove_labels retires a
// ledger row (W-IssuePatch.Labels) without adopting a single case. A retired
// refusal with no positive assertion behind it is exactly the shape that lets a
// member stop being refused and never start being sent.
//
// WHAT ONLY A SERVER CAN SAY is the ORDER. The client sends three flat members
// and the server assembles them into ONE issueops.LabelPatch, applying replace,
// then add, then remove — so REMOVAL WINS where a label appears in more than
// one. That algebra is never this client's to arrange, and the only way to know
// the three members reach one patch rather than three sequential edits is to
// send a request whose arms CONTRADICT each other and read the answer.
//
// The concurrent-writer case is the reason the pair exists at all: the add is
// read back against a label another writer added between the seed and the edit,
// which a read-modify-write onto the replace-only member would have dropped.
func TestServedUpdateAppliesTheOrderedLabelEditServerSide(t *testing.T) {
	env := newServedEnv(t, "hlbl")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	labels := func(id string) []string {
		t.Helper()
		row, err := env.getIssue(ctx, id)
		if err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		out := append([]string(nil), row.Labels...)
		sort.Strings(out)
		return out
	}
	seed := func(id string, initial ...string) {
		t.Helper()
		if err := env.createIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Labels: initial,
		}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	t.Run("an incremental add keeps what another writer added", func(t *testing.T) {
		const id = "hlbl-add"
		seed(id, "original")
		// The concurrent writer, out of band: a client composing this edit from a
		// read and a whole-set write would have read the set BEFORE this landed.
		if err := env.exec(ctx, []conformance.SQLStatement{
			{Query: "INSERT INTO labels (issue_id, label) VALUES (?, ?)", Args: []any{id, "meanwhile"}},
		}); err != nil {
			t.Fatalf("seed the concurrent label: %v", err)
		}

		if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
			Actor: "writer", IssueID: id,
			Patch: issueops.IssuePatch{Labels: issueops.LabelPatch{Add: []string{"mine"}}},
		}); err != nil {
			t.Fatalf("add a label to %s: %v", id, err)
		}
		if got, want := labels(id), []string{"meanwhile", "mine", "original"}; !slices.Equal(got, want) {
			t.Errorf("%s carries %v, want %v: an incremental add adds, and drops nothing it did not name", id, got, want)
		}
	})

	t.Run("an incremental remove leaves the rest alone", func(t *testing.T) {
		const id = "hlbl-remove"
		seed(id, "keep", "drop")

		if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
			Actor: "writer", IssueID: id,
			Patch: issueops.IssuePatch{Labels: issueops.LabelPatch{Remove: []string{"drop", "never-carried"}}},
		}); err != nil {
			t.Fatalf("remove a label from %s: %v", id, err)
		}
		if got, want := labels(id), []string{"keep"}; !slices.Equal(got, want) {
			t.Errorf("%s carries %v, want %v: removing a label the row does not carry changes nothing and is not an error", id, got, want)
		}
	})

	t.Run("removal wins over replace and add in one request", func(t *testing.T) {
		const id = "hlbl-order"
		seed(id, "stale")

		// Every arm names `contested`, which is what makes the order OBSERVABLE:
		// replace-then-add-then-remove leaves it off, and any other order leaves
		// it on. `survivor` is the control — added and never removed — so a
		// request the server dropped whole cannot pass.
		if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
			Actor: "writer", IssueID: id,
			Patch: issueops.IssuePatch{Labels: issueops.LabelPatch{
				Replace: issueops.Field[[]string]{Set: true, Value: []string{"contested", "base"}},
				Add:     []string{"contested", "survivor"},
				Remove:  []string{"contested"},
			}},
		}); err != nil {
			t.Fatalf("apply the ordered edit to %s: %v", id, err)
		}
		if got, want := labels(id), []string{"base", "survivor"}; !slices.Equal(got, want) {
			t.Errorf("%s carries %v, want %v: the three members are ONE patch applied replace, add, remove — "+
				"`contested` is named by all three and removal wins, and `stale` is gone because the replace ran first", id, got, want)
		}
	})
}

// TestServedUpdateFieldLengthBoundsAreTheServersAndNotThisClients is
// L-update-fieldlen's pin, and it RUNS.
//
// The bound is the same one on both sides — types.MaxFieldLen, checked with the
// same types.CheckFieldLen — and that is what makes the divergence a pure
// CLASSIFICATION loss rather than a difference about what may be stored. The
// operation applies it at the edge, before the role, and spells the refusal as
// the `invalid_argument`/`invalid_value` pair every malformed member on the
// patch earns, so nothing on the wire tells "too long" from "not a status" and
// reconstructing ErrFieldTooLong would misclassify every other refusal of the
// same member.
//
// THE CEILING IS ASSERTED FIRST, for the reason the config-bounds pin gives:
// without it the case would pass against a client that refused every label, and
// what makes this a bound rather than a blanket is that exactly MaxFieldLen
// characters land.
//
// The reference store is driven through its OWN Lifecycle role so "the two legs
// differ" is OBSERVED rather than asserted — and so the day the two agree, this
// fails and the row retires rather than outliving its reason.
func TestServedUpdateFieldLengthBoundsAreTheServersAndNotThisClients(t *testing.T) {
	env := newServedEnv(t, "hlfl")
	ctx := t.Context()
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	reference, err := env.reference.IssueLifecycle()
	if err != nil {
		t.Fatalf("the reference store's IssueLifecycle(): %v", err)
	}

	const subject, control = "hlfl-subject", "hlfl-control"
	seedServedIssue(t, ctx, env, subject, types.StatusOpen)
	seedServedIssue(t, ctx, env, control, types.StatusOpen)

	atCeiling := strings.Repeat("x", types.MaxFieldLen)
	past := strings.Repeat("x", types.MaxFieldLen+1)
	addLabel := func(id, label string) issueops.UpdateRequest {
		return issueops.UpdateRequest{Actor: "writer", IssueID: id, Patch: issueops.IssuePatch{
			Labels: issueops.LabelPatch{Add: []string{label}},
		}}
	}

	if _, err := lifecycle.Update(ctx, addLabel(subject, atCeiling)); err != nil {
		t.Fatalf("a label of exactly %d characters was refused: %v", types.MaxFieldLen, err)
	}

	overlong := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("a member one character past the column = %v, want ErrValidation carried back from the operation's 400", err)
		}
		if errors.Is(err, types.ErrFieldTooLong) {
			t.Error("the refusal carries ErrFieldTooLong; L-update-fieldlen says the wire's invalid_value cannot express it — retire the row rather than the assertion")
		}
	}
	_, err = lifecycle.Update(ctx, addLabel(subject, past))
	overlong(t, err)
	_, err = lifecycle.Update(ctx, issueops.UpdateRequest{Actor: "writer", IssueID: subject, Patch: issueops.IssuePatch{
		Title: issueops.Field[string]{Set: true, Value: past},
	}})
	overlong(t, err)

	// Nothing landed. A column that truncates silently is the failure this
	// bound exists to prevent, so the check is for a row with the prefix rather
	// than for the value itself.
	var truncated int
	if err := env.queryScalar(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", []any{subject, past}, &truncated); err != nil {
		t.Fatalf("look for the refused label on %s: %v", subject, err)
	}
	if truncated != 0 {
		t.Errorf("%s carries %d row(s) for the refused label, want none", subject, truncated)
	}

	// The other leg, through its own role: the same request is ErrFieldTooLong
	// there. The day it stops being, the two legs agree and the row is done.
	if _, refErr := reference.Update(ctx, addLabel(control, past)); !errors.Is(refErr, types.ErrFieldTooLong) {
		t.Errorf("the reference store answered %v for the same over-long label, want ErrFieldTooLong; "+
			"if the two legs now classify it alike, L-update-fieldlen has retired and this pin should go with it", refErr)
	}
}
