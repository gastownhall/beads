//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_ready_claimer_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The ReadyClaimer contract against the served surface, and the tier is WHOLE:
// eleven entrypoints, nothing parked.
//
// THIS FILE USED TO OPEN BY CALLING THE ROLE COMPOSED, and the sentence is worth
// keeping as a record rather than deleting: "the wire publishes no claimNext, so
// a claim of the next ready row is a listing followed by claims down it". That
// stopped being true when upstream published POST /v0/beads/issues:claimNext
// (#5510), and client wave ga-jpywb dials it — so what the contract runs against
// here is ONE atomic operation, and the two parks the composition owed are gone
// with it.
//
// The composition SURVIVES as the down-level leg, capability-gated exactly as
// BatchCloser's does, so `bd ready --claim` keeps working against a server older
// than the operation. That leg is not what this file measures — a server that
// advertises the token never reaches it — and its own coverage is the stub-driven
// unit case beside L14.
//
// THREE OF THESE CASES USED TO PARK on the reader accessor: each establishes
// its control by asking this backend's OWN Reader.Ready what the front holds,
// and the reader was another bead's flip. That bead has landed, so the reader
// is bound below and the three run for real — which is what the park said would
// happen, and is why a park's retirement condition is worth writing down.
//
// THE OTHER TWO PARKS WERE THE COMPOSITION'S, and both retire here for the same
// reason rather than for two: a wisp id was not claimable through `claimIssue`,
// and a lease was written by a transaction the client never entered. The claim is
// now the SERVER's role in one transaction, so an ephemeral row the filter admits
// is claimed and a durable win writes its lease — neither of which was ever a
// fact about the wire's vocabulary, only about what a two-call composition could
// reach.

func newServedReadyClaimerFixture(t *testing.T, prefix string) conformance.ReadyClaimerFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	claimer, err := env.subject.ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer(): %v", err)
	}
	reader, err := env.subject.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	return conformance.ReadyClaimerFixture{
		IssuePrefix: env.prefix,
		Claimer:     claimer,
		// The SUBJECT's reader, deliberately, not the reference store's: the
		// cases that use it compare two of this backend's surfaces against each
		// other, and binding the server's reader would compare the server to
		// itself and prove nothing about the client.
		Reader:        reader,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		AddDependency: env.addDependency,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
	}
}

func TestServedReadyClaimerRejectsLimitOffsetBriefAndEmptyActor(t *testing.T) {
	conformance.RunReadyClaimerRejectsLimitOffsetBriefAndEmptyActor(t, t.Context(), newServedReadyClaimerFixture(t, "hrc1"))
}

func TestServedReadyClaimerEmptyFrontIsNormal(t *testing.T) {
	conformance.RunReadyClaimerEmptyFrontIsNormal(t, t.Context(), newServedReadyClaimerFixture(t, "hrc2"))
}

func TestServedReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState(t *testing.T) {
	conformance.RunReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState(t, t.Context(), newServedReadyClaimerFixture(t, "hrc3"))
}

// TestServedReadyClaimerClaimsAnEphemeralRowTheFilterAdmits was PARKED for as
// long as the role was composed, and the park named its own retirement: "the
// composition cannot bridge that; a claimNext operation would." It did.
func TestServedReadyClaimerClaimsAnEphemeralRowTheFilterAdmits(t *testing.T) {
	conformance.RunReadyClaimerClaimsAnEphemeralRowTheFilterAdmits(t, t.Context(), newServedReadyClaimerFixture(t, "hrc4"))
}

func TestServedReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet(t *testing.T) {
	conformance.RunReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet(t, t.Context(), newServedReadyClaimerFixture(t, "hrc5"))
}

// TestServedReadyClaimerLeasesADurableWinButNotAnEphemeralOne runs now, and the
// park it replaces was half right about why it could not.
//
// "The wire has no lease vocabulary" is still true and was never the obstacle:
// the case reads the lease table OUT OF BAND, through the reference store, so
// what it needs is a claim that WRITES one — which the server's own role does
// inside the claiming transaction. What actually blocked it was the ephemeral
// half, and that was the composition's limit rather than the wire's.
func TestServedReadyClaimerLeasesADurableWinButNotAnEphemeralOne(t *testing.T) {
	conformance.RunReadyClaimerLeasesADurableWinButNotAnEphemeralOne(t, t.Context(), newServedReadyClaimerFixture(t, "hrc6"))
}

// TestServedReadyClaimerAnswersTheQuestionReaderReadyLists asks BOTH of this
// backend's surfaces one question and compares them. It is the strongest case
// in the file now that both are wired: the listing and the claim reach the
// server through two different operations and two different encodings, so an
// agreement here is an agreement about the CLIENT rather than about the server.
func TestServedReadyClaimerAnswersTheQuestionReaderReadyLists(t *testing.T) {
	conformance.RunReadyClaimerAnswersTheQuestionReaderReadyLists(t, t.Context(), newServedReadyClaimerFixture(t, "hrc7"))
}

// TestServedReadyClaimerSkipsIneligibleFrontRows is the case with the most to
// say about the composition: the loop must walk past rows a racing agent
// already took instead of reporting an empty front. It establishes its control
// by asking Reader.Ready that the taken rows are on the front at all, which is
// what it needed the reader for.
// TestReadyClaimWalksPastLostRacesAndHydratesFromThePage drives the same loop
// against a stub transport; this one drives it against a server.
func TestServedReadyClaimerSkipsIneligibleFrontRows(t *testing.T) {
	conformance.RunReadyClaimerSkipsIneligibleFrontRows(t, t.Context(), newServedReadyClaimerFixture(t, "hrc8"))
}

func TestServedReadyClaimerRecordsOneHistoryEntryForAWin(t *testing.T) {
	conformance.RunReadyClaimerRecordsOneHistoryEntryForAWin(t, t.Context(), newServedReadyClaimerFixture(t, "hrc9"))
}

func TestServedReadyClaimerDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunReadyClaimerDoesNotMutateTheCallerRequest(t, t.Context(), newServedReadyClaimerFixture(t, "hrca"))
}

// TestServedReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven is
// the dropped-filter case, and it is the one that says most about the port: the
// filter no longer travels as a page this client walks, it travels as the query
// string the server's own ready decode reads — the SAME function GET
// /v0/beads/ready goes through. A member this client failed to encode would take
// the top-priority decoy every arm is built around.
func TestServedReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven(t *testing.T) {
	conformance.RunReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven(t, t.Context(), newServedReadyClaimerFixture(t, "hrcb"))
}

// TestServedReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities is the
// case the composition could never have passed honestly.
//
// The composed claim took its cardinalities from the READY PAGE and its row from
// the claim response, which L14's third residue recorded as staleness bounded by
// the fetch-to-dial window. The operation hydrates the winner INSIDE the
// committing transaction, so the counts describe the state the claim produced —
// and the client's only job is not to recompute them.
func TestServedReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities(t *testing.T) {
	conformance.RunReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities(t, t.Context(), newServedReadyClaimerFixture(t, "hrcc"))
}
