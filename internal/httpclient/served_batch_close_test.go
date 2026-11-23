//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_batch_close_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The BatchCloser contract against the served surface.
//
// The client sends the whole CloseBatchRequest on one issues:batchClose call,
// and the reference store answers with the per-item outcomes. The transactional
// ClaimNext the storage-level contract composes onto CloseBatch has NO v0 wire
// expression: BatchCloseRequest/BatchCloseResponse publish no
// claim_next/claimed_next member, so the client refuses any non-nil
// req.ClaimNext (D8 refuse-not-drop) rather than silently dropping the claim.
// The seven cases that exercise a real ClaimNext are parked with
// skipKnownDivergence citing W-CloseBatchRequest.ClaimNext; everything else —
// including the two cases that merely validate a ClaimNext filter's shape
// before any capability question is reached — runs for real.
//
// Seeding still never goes through the client: every fixture takes its seed and
// post-state hooks from the reference store the server serves, and binds only the
// BatchCloser under test to the http client (checkFixtureBinding).

func newServedBatchCloserFixture(t *testing.T, prefix string) conformance.BatchCloserFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	closer, err := env.subject.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser(): %v", err)
	}
	return conformance.BatchCloserFixture{
		IssuePrefix:   env.prefix,
		Closer:        closer,
		CreateIssue:   env.createIssue,
		AddDependency: env.addDependency,
		CreateWisp:    env.createWisp,
		AddComment:    env.reference.AddComment,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
		// The message-scoped hook the wisp-naming case needs. Without it that
		// case skips as UNPINNED, which is a weaker claim than this tier can
		// make: the reference store behind the composition is embedded Dolt, so
		// its log is readable BY MESSAGE and the clause is checkable here.
		CountHistoryMatching: env.countHistoryMatching,
	}
}

func TestServedBatchCloserOutcomeSnapshotIsTheDocumentedShape(t *testing.T) {
	conformance.RunBatchCloserOutcomeSnapshotIsTheDocumentedShape(t, t.Context(), newServedBatchCloserFixture(t, "hbcsnap"))
}

func TestServedBatchCloserBackendFailureReturnsNoOutcomes(t *testing.T) {
	conformance.RunBatchCloserBackendFailureReturnsNoOutcomes(t, t.Context(), newServedBatchCloserFixture(t, "hbcfail"))
}

func TestServedBatchCloserOutcomesMirrorItemsIndexForIndex(t *testing.T) {
	conformance.RunBatchCloserOutcomesMirrorItemsIndexForIndex(t, t.Context(), newServedBatchCloserFixture(t, "hbcmir"))
}

func TestServedBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit(t *testing.T) {
	conformance.RunBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit(t, t.Context(), newServedBatchCloserFixture(t, "hbcperitem"))
}

func TestServedBatchCloserRequestValidationReturnsZeroResultAndChangesNothing(t *testing.T) {
	conformance.RunBatchCloserRequestValidationReturnsZeroResultAndChangesNothing(t, t.Context(), newServedBatchCloserFixture(t, "hbcval"))
}

func TestServedBatchCloserClaimFilterValueFailureIsARequestValidationFailure(t *testing.T) {
	conformance.RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure(t, t.Context(), newServedBatchCloserFixture(t, "hbccfilt"))
}

func TestServedBatchCloserIdempotentRecloseIsAPerItemSuccess(t *testing.T) {
	conformance.RunBatchCloserIdempotentRecloseIsAPerItemSuccess(t, t.Context(), newServedBatchCloserFixture(t, "hbcidem"))
}

func TestServedBatchCloserAllIdempotentBatchLandsNothing(t *testing.T) {
	skipKnownDivergence(t, "W-CloseBatchRequest.ClaimNext", parkBead,
		"the case drives a real ClaimNext through CloseBatch; the v0 wire's "+
			"BatchCloseRequest/BatchCloseResponse publish no claim_next/claimed_next "+
			"member, so the client refuses it (D8 refuse-not-drop) rather than silently "+
			"dropping the claim")
	conformance.RunBatchCloserAllIdempotentBatchLandsNothing(t, t.Context(), newServedBatchCloserFixture(t, "hbcallid"))
}

func TestServedBatchCloserDuplicateItemRecloseAtItsOwnIndex(t *testing.T) {
	conformance.RunBatchCloserDuplicateItemRecloseAtItsOwnIndex(t, t.Context(), newServedBatchCloserFixture(t, "hbcdup"))
}

func TestServedBatchCloserWispItemClosesAndEarnsTheClaim(t *testing.T) {
	skipKnownDivergence(t, "W-CloseBatchRequest.ClaimNext", parkBead,
		"the case asserts the wisp close earns a real ClaimNext; the v0 wire has no "+
			"claim_next/claimed_next member on BatchClose, so the client refuses the "+
			"request rather than earning a claim it cannot report back")
	conformance.RunBatchCloserWispItemClosesAndEarnsTheClaim(t, t.Context(), newServedBatchCloserFixture(t, "hbcwisp"))
}

func TestServedBatchCloserDurableHistoryNeverNamesAWisp(t *testing.T) {
	conformance.RunBatchCloserDurableHistoryNeverNamesAWisp(t, t.Context(), newServedBatchCloserFixture(t, "hbcwhist"))
}

func TestServedBatchCloserForceBypassesOnlyClosePolicy(t *testing.T) {
	conformance.RunBatchCloserForceBypassesOnlyClosePolicy(t, t.Context(), newServedBatchCloserFixture(t, "hbcforce"))
}

func TestServedBatchCloserClaimNextHydratesWhenSomethingClosed(t *testing.T) {
	skipKnownDivergence(t, "W-CloseBatchRequest.ClaimNext", parkBead,
		"the case needs CloseBatch to hydrate a real ClaimNext result; the v0 wire "+
			"carries no claim_next/claimed_next member, so the client refuses rather "+
			"than hydrating a claim from nothing")
	conformance.RunBatchCloserClaimNextHydratesWhenSomethingClosed(t, t.Context(), newServedBatchCloserFixture(t, "hbccnhy"))
}

func TestServedBatchCloserClaimNextIsNilWhenNothingClosed(t *testing.T) {
	skipKnownDivergence(t, "W-CloseBatchRequest.ClaimNext", parkBead,
		"the case asserts a nil ClaimedNext after a real ClaimNext request landed "+
			"nothing to close; the v0 wire has no member to carry the request at all, "+
			"so the client refuses it before the nil-vs-populated question is reachable")
	conformance.RunBatchCloserClaimNextIsNilWhenNothingClosed(t, t.Context(), newServedBatchCloserFixture(t, "hbccnno"))
}

func TestServedBatchCloserClaimNextIsNilWhenTheFrontIsEmpty(t *testing.T) {
	skipKnownDivergence(t, "W-CloseBatchRequest.ClaimNext", parkBead,
		"same as the other ClaimNext-on-CloseBatch cases: the v0 wire has no "+
			"claim_next/claimed_next member, so the client refuses the request")
	conformance.RunBatchCloserClaimNextIsNilWhenTheFrontIsEmpty(t, t.Context(), newServedBatchCloserFixture(t, "hbccnempty"))
}

func TestServedBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch(t *testing.T) {
	skipKnownDivergence(t, "W-CloseBatchRequest.ClaimNext", parkBead,
		"the case needs the batch's own unblocking to be visible to a ClaimNext run "+
			"in the same request; the v0 wire cannot carry ClaimNext on CloseBatch at "+
			"all, so the client refuses before the ordering question is reachable")
	conformance.RunBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch(t, t.Context(), newServedBatchCloserFixture(t, "hbccnunb"))
}

func TestServedBatchCloserRecordsOneHistoryEntryForWhatLanded(t *testing.T) {
	conformance.RunBatchCloserRecordsOneHistoryEntryForWhatLanded(t, t.Context(), newServedBatchCloserFixture(t, "hbc1hist"))
}

func TestServedBatchCloserAllRefusedBatchRecordsNoHistory(t *testing.T) {
	conformance.RunBatchCloserAllRefusedBatchRecordsNoHistory(t, t.Context(), newServedBatchCloserFixture(t, "hbcnohist"))
}

func TestServedBatchCloserDoesNotMutateTheCallerRequest(t *testing.T) {
	skipKnownDivergence(t, "W-CloseBatchRequest.ClaimNext", parkBead,
		"the case populates req.ClaimNext with a real ready filter and closes for "+
			"real to prove the caller's request pointer survives untouched; the v0 "+
			"wire cannot carry ClaimNext on CloseBatch, so the client refuses before "+
			"any closing happens")
	conformance.RunBatchCloserDoesNotMutateTheCallerRequest(t, t.Context(), newServedBatchCloserFixture(t, "hbcnomut"))
}

// TestServedBatchCloserSettlesTheDependersOfWhatItClosed is the blocked-state
// postcondition, and the only case here whose observable is a column no read on
// this surface hydrates — so it goes to the reference store directly.
//
// It is the batch's own version of the clause the single close carries, and the
// batch is where it is easiest to get wrong: two items land in ONE transaction,
// so a body that settled per item, or settled only the items it walked last,
// leaves a depender of the first item marked blocked by a row that is closed.
func TestServedBatchCloserSettlesTheDependersOfWhatItClosed(t *testing.T) {
	conformance.RunBatchCloserSettlesTheDependersOfWhatItClosed(t, t.Context(), newServedBatchCloserFixture(t, "hbcbs"))
}

// TestServedBatchCloserItemsAnswerToTheCloseGuards is the served leg of the
// close guards: each guarded item travels back as its outcome's code (and
// `assignee`) and is rebuilt into the same typed error a local batch returns.
func TestServedBatchCloserItemsAnswerToTheCloseGuards(t *testing.T) {
	conformance.RunBatchCloserItemsAnswerToTheCloseGuards(t, t.Context(), newServedBatchCloserFixture(t, "hbcgd"))
}
