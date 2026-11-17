//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_metadata_cas_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The MetadataCAS contracts, run through client → in-process bd serve →
// reference store.
//
// EVERY ONE OF THEM RUNS. That is worth saying out loud on a role wired by a
// client wave, because it is the exception here rather than the rule: the
// create, delete and batch-create families each park cases on a refusal
// vocabulary the wire flattens or on a member its body has no place for, and
// this one parks nothing. The request maps whole — actor, the id in the path,
// the key, and the two sides of the transition — and both members of the result
// are published, so there is no shape for a divergence to live in.
//
// WHAT THE COMPOSITION BUYS HERE that the three in-tree legs cannot. All three
// of those run one body (internal/storage/issueops.CompareAndSetMetadataKeyInTx)
// through different transaction wrappers, so the tier is "one reading plus two
// wrapper checks" by its own header note. This leg adds a fourth wrapper that is
// a NETWORK: the request is serialized, the absent-versus-null distinction on
// three members has to survive a JSON round trip in both directions, and the
// verdict comes back inside a 200. Those are exactly the states the contract's
// cases are built to separate, and none of the in-tree legs can lose them.

func newServedMetadataCASFixture(t *testing.T, prefix string) conformance.MetadataCASFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	cas, err := env.subject.MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS(): %v", err)
	}
	return conformance.MetadataCASFixture{
		IssuePrefix:   env.prefix,
		MetadataCAS:   cas,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
		CommitPending: env.commitPending,
	}
}

// The transition, stated as a pair: nil on either side means the key is ABSENT
// there, so a first-writer-wins acquire is (nil → value) and a release is
// (value → nil).

func TestServedMetadataCASCreatesAKeyThatWasAbsent(t *testing.T) {
	conformance.RunMetadataCASCreatesAKeyThatWasAbsent(t, t.Context(), newServedMetadataCASFixture(t, "hmc1"))
}

func TestServedMetadataCASRefusesASecondCreateAndReportsTheHolder(t *testing.T) {
	conformance.RunMetadataCASRefusesASecondCreateAndReportsTheHolder(t, t.Context(), newServedMetadataCASFixture(t, "hmc2"))
}

func TestServedMetadataCASSwapsOnAMatchAndReportsTheNewValue(t *testing.T) {
	conformance.RunMetadataCASSwapsOnAMatchAndReportsTheNewValue(t, t.Context(), newServedMetadataCASFixture(t, "hmc3"))
}

// TestServedMetadataCASRefusalReportsTheCurrentValueAndWritesNothing is the case
// this seam could most easily have got wrong: a lost race is a 200, so a client
// that classified the answer by status code would report it as a won swap.
func TestServedMetadataCASRefusalReportsTheCurrentValueAndWritesNothing(t *testing.T) {
	conformance.RunMetadataCASRefusalReportsTheCurrentValueAndWritesNothing(t, t.Context(), newServedMetadataCASFixture(t, "hmc4"))
}

func TestServedMetadataCASComparesCanonically(t *testing.T) {
	conformance.RunMetadataCASComparesCanonically(t, t.Context(), newServedMetadataCASFixture(t, "hmc5"))
}

// TestServedMetadataCASReportsTheValueTheRowHolds is the loop's convergence
// clause: Current is read from the ROW rather than echoed from the request, so a
// caller that feeds it back as the next Expected converges even where the store
// renormalized what it wrote.
func TestServedMetadataCASReportsTheValueTheRowHolds(t *testing.T) {
	conformance.RunMetadataCASReportsTheValueTheRowHolds(t, t.Context(), newServedMetadataCASFixture(t, "hmc6"))
}

// TestServedMetadataCASDistinguishesAnAbsentKeyFromAStoredNull is the one the
// WIRE makes hard, and the reason the response's `current` is a bare
// json.RawMessage rather than a pointer: encoding/json answers a JSON null
// against a pointer by setting it nil before any UnmarshalJSON runs, so a
// present null and an omitted member would decode identically. They mean
// opposite things here, and a retry loop that confused them would swap with
// `expected` omitted, mismatch, and never converge — a livelock on a stream of
// 200s.
func TestServedMetadataCASDistinguishesAnAbsentKeyFromAStoredNull(t *testing.T) {
	conformance.RunMetadataCASDistinguishesAnAbsentKeyFromAStoredNull(t, t.Context(), newServedMetadataCASFixture(t, "hmc7"))
}

func TestServedMetadataCASRemovesTheKeyWhenTheValueIsAbsent(t *testing.T) {
	conformance.RunMetadataCASRemovesTheKeyWhenTheValueIsAbsent(t, t.Context(), newServedMetadataCASFixture(t, "hmc8"))
}

func TestServedMetadataCASPreservesSiblingKeys(t *testing.T) {
	conformance.RunMetadataCASPreservesSiblingKeys(t, t.Context(), newServedMetadataCASFixture(t, "hmc9"))
}

func TestServedMetadataCASNoOpSwapWritesNothing(t *testing.T) {
	conformance.RunMetadataCASNoOpSwapWritesNothing(t, t.Context(), newServedMetadataCASFixture(t, "hmca"))
}

// The refusals. Both arrive as the sentinel a local backend returns for the same
// refusal: the 404 for an id on neither plane, and the 400 for a request the
// role's own rules reject.

func TestServedMetadataCASRefusesAnIDOnNeitherPlane(t *testing.T) {
	conformance.RunMetadataCASRefusesAnIDOnNeitherPlane(t, t.Context(), newServedMetadataCASFixture(t, "hmcb"))
}

func TestServedMetadataCASRefusesAnUnusableRequest(t *testing.T) {
	conformance.RunMetadataCASRefusesAnUnusableRequest(t, t.Context(), newServedMetadataCASFixture(t, "hmcc"))
}

// The planes and the version-control clauses.

func TestServedMetadataCASResolvesAWispAnchor(t *testing.T) {
	conformance.RunMetadataCASResolvesAWispAnchor(t, t.Context(), newServedMetadataCASFixture(t, "hmcd"))
}

func TestServedMetadataCASAWispSwapRecordsNoDurableHistory(t *testing.T) {
	conformance.RunMetadataCASAWispSwapRecordsNoDurableHistory(t, t.Context(), newServedMetadataCASFixture(t, "hmce"))
}

func TestServedMetadataCASRecordsExactlyOneHistoryEntry(t *testing.T) {
	conformance.RunMetadataCASRecordsExactlyOneHistoryEntry(t, t.Context(), newServedMetadataCASFixture(t, "hmcf"))
}

// TestServedMetadataCASHistoryEntryNamesTheActor is why Actor is required on
// this request and why this client refuses an empty one before dialing: a swap
// is a coordination write between racing callers, and the one question asked of
// its trace afterwards is which of them won. No result member carries the actor,
// so every other case here would pass with it dropped between the client and the
// row.
func TestServedMetadataCASHistoryEntryNamesTheActor(t *testing.T) {
	conformance.RunMetadataCASHistoryEntryNamesTheActor(t, t.Context(), newServedMetadataCASFixture(t, "hmcg"))
}

func TestServedMetadataCASARefusedSwapRecordsNoHistory(t *testing.T) {
	conformance.RunMetadataCASARefusedSwapRecordsNoHistory(t, t.Context(), newServedMetadataCASFixture(t, "hmch"))
}

func TestServedMetadataCASDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunMetadataCASDoesNotMutateTheCallerRequest(t, t.Context(), newServedMetadataCASFixture(t, "hmci"))
}
