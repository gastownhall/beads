package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the contract every implementation of publicops.MetadataCAS
// must satisfy. Each case asserts what issueops/metadatacas.go PROMISES, cited
// by symbol, rather than what any one backend happens to do; a backend that
// genuinely disagrees is parked at its own wiring site with skipKnownDivergence
// so the case still runs on the ones that agree.
//
// THERE IS ONE BODY BEHIND THE THREE WIRINGS, and this is the second role after
// TreeWalker where that is true. All three legs run
// internal/storage/issueops.CompareAndSetMetadataKeyInTx: the two Dolt-backed
// stores wrap it in their own transaction and the unit-of-work provider reaches
// it through the domain issue repository. So the three-leg run is ONE reading
// plus two wrapper checks, never "three backends agree".
//
// THE CASES ARE WRITTEN FOR THAT. What a per-leg failure would actually be is a
// WRAPPER losing something: a request field dropped on the way down, a
// transaction not opened, a version-control entry recorded for a swap that
// wrote nothing, a refusal that stops matching errors.Is. So the cases assert
// SENTINELS rather than message text, read RAW ROWS rather than asking the role
// what it just did, and take history DELTAS around the call. The parts that
// decide what the answer MEANS — the canonical equality rule and the request
// refusals — are pure functions pinned without a database in
// internal/storage/metadata_cas_test.go; what is left here is what only a real
// backend can show.
//
// WHAT THIS CONTRACT DELIBERATELY DOES NOT PIN, AND WHY — two promises, two
// different reasons, both of them measured rather than assumed.
//
// THE STORED SIDE OF THE EQUALITY RULE. The role canonicalizes the value it
// READ as well as the one the caller sent, so an expectation can match a value
// some other writer spelled awkwardly. On every in-tree backend that half is
// unobservable, because the metadata column is a Dolt JSON column and the
// ENGINE normalizes on the way in: a seed of
// {"seeded":{"b":2,"a":1},"num":1.0,"spaced":{ "z" :  3 }} comes back as
// {"num": 1, "seeded": {"a": 1, "b": 2}, "spaced": {"z": 3}}. Nested key order,
// whitespace and even a trailing .0 are settled before the role ever sees them,
// so deleting the stored-side canonicalization passes every case here — it was
// measured, not guessed. A case written for it would be green forever, which is
// worse than no case: a reviewer greps for the promise, finds a test named for
// it, and stops looking. What would upgrade this is a backend whose metadata
// column is TEXT rather than JSON; the caller's side is pinned without a
// database beside the plan function.
//
// THE ONE-TRANSACTION PROMISE.
// It is structural rather than black-box observable — a single-threaded case
// cannot falsify it, because one transaction and two produce identical answers
// when nothing else is writing, and a concurrent case would be flaky at three
// engines, buying a red suite people learn to re-run rather than a guarantee.
// What holds it instead is the SHAPE of the body: there is no two-call
// composition to regress into without deleting the …InTx function, and every
// leg's accessor hands it a transaction it did not open itself. The probe that
// would upgrade this is a transaction-counting seam on the fixture kit. Do not
// fake it with sleeps.
//
// EVERY CASE NAMESPACES ITS IDS with the fixture's IssuePrefix and its own tag,
// because the legs share one database across a role's cases, and metadata keys
// are per-issue so a fresh id is a fresh key space.

// MetadataCASFixture supplies adapter-specific storage access for the
// compare-and-set assertions. Every field but the last two is named and typed
// exactly like the per-backend roleFixtureKit hook it is filled from.
type MetadataCASFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// MetadataCAS is the surface under test.
	MetadataCAS publicops.MetadataCAS
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane. It is a separate
	// field rather than an Ephemeral flag on CreateIssue because the three
	// adapters reach the two planes through different verbs.
	CreateWisp func(context.Context, *types.Issue, string) error
	// QueryScalar runs a single-row query and scans it. It is how these cases
	// read the metadata COLUMN, which is the only way to tell "the answer looks
	// right" from "the row is right": reading a value back through the role is
	// exactly the check that passes on a body that never wrote.
	QueryScalar func(context.Context, string, []any, ...any) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the cases
	// that need it SKIP with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
	// CommitPending puts everything written so far into the version history, so
	// a later CountHistory delta measures the call under test and not the seed
	// that led up to it. It is built at each wiring site over a seam the
	// backend already publishes — out of band, like SweeperFixture.CommitPending
	// — because the frozen kit reaches the issues and config planes only.
	//
	// A nil hook means the backend cannot settle its history on demand, and the
	// cases that need it SKIP with that reason.
	CommitPending func(context.Context) error
}

// RunMetadataCASCreatesAKeyThatWasAbsent pins the transition a first-writer
// protocol is built from (CompareAndSetKeyRequest.Expected): nil Expected means
// the key must be ABSENT, and a swap against an absent key lands.
//
// It reads the metadata COLUMN afterwards rather than asking the role, because
// a body that answered Swapped and wrote nothing would satisfy every assertion
// made through the role.
func RunMetadataCASCreatesAKeyThatWasAbsent(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "create", false)

	result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor:   "cas-tester",
		IssueID: id,
		Key:     "gc.lease",
		Value:   metadataCASValue(`"holder-a"`),
	})
	if !result.Swapped {
		t.Fatalf("CompareAndSetKey(absent -> value) Swapped = false, want the swap to land on an absent key")
	}
	metadataCASAssertCurrent(t, result, `"holder-a"`)
	metadataCASAssertStored(t, ctx, fixture, id, "gc.lease", `"holder-a"`)
}

// RunMetadataCASRefusesASecondCreateAndReportsTheHolder pins the whole point of
// the role, on the shape a lease acquisition actually has: the second caller to
// ask for an absent key loses, learns nothing was written, and is handed the
// value that beat it so its retry does not need a second read.
//
// A LOST RACE IS NOT AN ERROR — see MetadataCAS.CompareAndSetKey — so this case
// asserts a nil error as hard as it asserts the refusal.
func RunMetadataCASRefusesASecondCreateAndReportsTheHolder(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "race", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-winner", IssueID: id, Key: "gc.lease", Value: metadataCASValue(`"holder-a"`),
	})

	result, err := fixture.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
		Actor: "cas-loser", IssueID: id, Key: "gc.lease", Value: metadataCASValue(`"holder-b"`),
	})
	if err != nil {
		t.Fatalf("a lost race returned error = %v, want nil: a mismatch is an answer, not a failure", err)
	}
	if result.Swapped {
		t.Fatal("the second create won; nil Expected must mean the key is ABSENT, not unchecked")
	}
	metadataCASAssertCurrent(t, result, `"holder-a"`)
	metadataCASAssertStored(t, ctx, fixture, id, "gc.lease", `"holder-a"`)
}

// RunMetadataCASSwapsOnAMatchAndReportsTheNewValue pins the ordinary
// value-to-value transition, and that Current afterwards describes the value
// the swap landed rather than the one it replaced.
func RunMetadataCASSwapsOnAMatchAndReportsTheNewValue(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "swap", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"start"`),
	})

	result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor:    "cas-tester",
		IssueID:  id,
		Key:      "phase",
		Expected: metadataCASValue(`"start"`),
		Value:    metadataCASValue(`{"step":2,"note":"running"}`),
	})
	if !result.Swapped {
		t.Fatal("CompareAndSetKey over a matching expectation refused; the precondition held")
	}
	metadataCASAssertCurrent(t, result, `{"note":"running","step":2}`)
	metadataCASAssertStored(t, ctx, fixture, id, "phase", `{"note":"running","step":2}`)
}

// RunMetadataCASRefusalReportsTheCurrentValueAndWritesNothing pins the refusal
// half of a value-to-value swap: a stale expectation loses, the stored value is
// untouched, and Current carries what refused it.
func RunMetadataCASRefusalReportsTheCurrentValueAndWritesNothing(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "stale", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"current"`),
	})

	result, err := fixture.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
		Actor:    "cas-tester",
		IssueID:  id,
		Key:      "phase",
		Expected: metadataCASValue(`"stale"`),
		Value:    metadataCASValue(`"clobbered"`),
	})
	if err != nil {
		t.Fatalf("a stale expectation returned error = %v, want nil", err)
	}
	if result.Swapped {
		t.Fatal("a stale expectation won the swap")
	}
	metadataCASAssertCurrent(t, result, `"current"`)
	metadataCASAssertStored(t, ctx, fixture, id, "phase", `"current"`)
}

// RunMetadataCASComparesCanonically pins the equality rule at the seam that
// matters — the caller's — rather than at the pure function that defines it:
// an Expected written with different whitespace and a different object key
// order still matches, so a caller cannot lose a swap to its own encoder.
//
// It also pins that Current comes back CANONICAL, which is what makes feeding
// it straight back as Expected a comparison of like with like.
func RunMetadataCASComparesCanonically(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "canon", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "shape", Value: metadataCASValue(`{"a":1,"b":[2,3]}`),
	})

	result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor:    "cas-tester",
		IssueID:  id,
		Key:      "shape",
		Expected: metadataCASValue("{ \"b\" : [2, 3],\n  \"a\": 1 }"),
		Value:    metadataCASValue(`"settled"`),
	})
	if !result.Swapped {
		t.Fatal("a re-formatted expectation lost the swap; equality is canonical, not byte-wise")
	}
	metadataCASAssertStored(t, ctx, fixture, id, "shape", `"settled"`)
}

// RunMetadataCASReportsTheValueTheRowHolds is the pin on what Current MEANS,
// and it is written around the fact that broke it: the metadata column decodes
// JSON numbers through float64 and re-emits them, so what a caller sends is not
// always what is stored.
//
// The three assertions are backend-INDEPENDENT on purpose. A backend that
// stores a number verbatim satisfies all of them and so does one that
// renormalizes; what none of them tolerates is a Current composed from the
// REQUEST, which is what makes the difference invisible and a retry loop
// non-convergent.
func RunMetadataCASReportsTheValueTheRowHolds(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "rowholds", false)

	const sent = `1.0`
	result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "token", Value: metadataCASValue(sent),
	})
	if !result.Swapped || result.Current == nil {
		t.Fatalf("CompareAndSetKey = %+v, want a swap carrying a value", result)
	}

	// 1. Current IS the row. Compared against the raw column rather than
	//    against what was sent, which is the whole point.
	stored, ok := metadataCASReadKey(t, ctx, fixture, "issues", id, "token")
	if !ok {
		t.Fatal("the swap landed but the key is absent from the column")
	}
	if string(*result.Current) != string(stored) {
		t.Errorf("Current = %s, but the column holds %s; Current must be read from the row, "+
			"never handed back from the request", string(*result.Current), string(stored))
	}

	// 2. A loop that feeds Current back CONVERGES. This is the documented
	//    pattern and the one that has to work on every substrate.
	if again := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "token",
		Expected: result.Current, Value: metadataCASValue(`"settled"`),
	}); !again.Swapped {
		t.Fatal("a swap expecting the Current a previous swap reported was refused; " +
			"the documented retry loop cannot converge")
	}

	// 3. And where the substrate DID renormalize, re-sending the caller's own
	//    literal is refused — the hazard the leaf tells callers to avoid by
	//    composing from Current and by preferring strings for tokens. On a
	//    substrate that stored the literal verbatim this arm is vacuous, which
	//    is why it is guarded rather than asserted flat.
	if string(stored) != sent {
		t.Logf("this backend renormalized %s to %s on the way in", sent, string(stored))
		metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
			Actor: "cas-tester", IssueID: id, Key: "token2", Value: metadataCASValue(sent),
		})
		lost := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
			Actor: "cas-tester", IssueID: id, Key: "token2",
			Expected: metadataCASValue(sent), Value: metadataCASValue(`"never"`),
		})
		if lost.Swapped {
			t.Error("re-sending a literal the substrate renormalized won the swap; " +
				"either the comparison stopped reading the row or the substrate changed")
		}
	}
}

// RunMetadataCASDistinguishesAnAbsentKeyFromAStoredNull pins the distinction
// the substrate can see and the role therefore reports: a key holding JSON null
// is PRESENT. A nil Expected must not match it, and an Expected of `null` must.
//
// This is the case that would go green on a body that decoded metadata into a
// map of concrete values and lost the difference on the way.
func RunMetadataCASDistinguishesAnAbsentKeyFromAStoredNull(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "null", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "slot", Value: metadataCASValue(`null`),
	})

	result, err := fixture.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "slot", Value: metadataCASValue(`"taken"`),
	})
	if err != nil {
		t.Fatalf("CompareAndSetKey(absent -> value) over a stored null error = %v, want nil", err)
	}
	if result.Swapped {
		t.Fatal("nil Expected matched a key stored holding null; a null key is PRESENT")
	}
	metadataCASAssertCurrent(t, result, `null`)

	result = metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "slot",
		Expected: metadataCASValue(`null`), Value: metadataCASValue(`"taken"`),
	})
	if !result.Swapped {
		t.Fatal("an Expected of null did not match a key stored holding null")
	}
	metadataCASAssertStored(t, ctx, fixture, id, "slot", `"taken"`)
}

// RunMetadataCASRemovesTheKeyWhenTheValueIsAbsent pins the transition that
// closes the loop (CompareAndSetKeyRequest.Value): a nil Value REMOVES the key,
// so a lease taken with a nil Expected can be returned to the state a
// subsequent acquire tests for.
//
// The proof is the re-acquire, not the delete: a body that stored JSON null
// instead of removing the key would pass a "the value is gone" check made
// through the role and fail this one.
func RunMetadataCASRemovesTheKeyWhenTheValueIsAbsent(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "release", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "gc.lease", Value: metadataCASValue(`"holder-a"`),
	})

	result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "gc.lease", Expected: metadataCASValue(`"holder-a"`),
	})
	if !result.Swapped {
		t.Fatal("the conditional delete was refused over a matching expectation")
	}
	if result.Current != nil {
		t.Errorf("Current after a delete = %s, want nil: nil means ABSENT", string(*result.Current))
	}
	metadataCASAssertAbsent(t, ctx, fixture, id, "gc.lease")

	// The re-acquire is what proves the key is absent rather than null.
	if again := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "gc.lease", Value: metadataCASValue(`"holder-b"`),
	}); !again.Swapped {
		t.Fatal("a create after a release was refused; the release must leave the key ABSENT")
	}
}

// RunMetadataCASPreservesSiblingKeys pins the clause that makes this role safe
// to use on a shared metadata object (MetadataCAS.CompareAndSetKey): the write
// re-serializes the object read INSIDE the transaction, so every other key
// survives byte-for-byte.
//
// It asserts through the raw column and through a second CAS on the sibling,
// because a body that dropped siblings and a body that stringified them look
// the same to a reader that only asks about the key it just wrote.
func RunMetadataCASPreservesSiblingKeys(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "siblings", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "keep", Value: metadataCASValue(`{"nested":["a","b"]}`),
	})
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "also", Value: metadataCASValue(`7`),
	})

	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "target", Value: metadataCASValue(`"set"`),
	})
	metadataCASAssertStored(t, ctx, fixture, id, "keep", `{"nested":["a","b"]}`)
	metadataCASAssertStored(t, ctx, fixture, id, "also", `7`)

	// A delete rewrites the object too, so it gets the same check.
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "target", Expected: metadataCASValue(`"set"`),
	})
	metadataCASAssertStored(t, ctx, fixture, id, "keep", `{"nested":["a","b"]}`)
	if result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "also",
		Expected: metadataCASValue(`7`), Value: metadataCASValue(`8`),
	}); !result.Swapped {
		t.Fatal("a sibling key no longer matched its own value after a neighbor was swapped and deleted")
	}
}

// RunMetadataCASNoOpSwapWritesNothing pins the clause that separates the
// VERDICT from the WRITE (MetadataCAS.CompareAndSetKey): when the precondition
// holds over a value already equal to the requested one, Swapped is true and
// nothing is touched.
//
// The absent-to-absent case is in here too, because it is the one shape where
// a body could plausibly decide it had something to write.
func RunMetadataCASNoOpSwapWritesNothing(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "noop", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"same"`),
	})
	before := metadataCASUpdatedAt(t, ctx, fixture, id)

	result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase",
		Expected: metadataCASValue(`"same"`), Value: metadataCASValue(`"same"`),
	})
	if !result.Swapped {
		t.Fatal("a value-to-itself swap reported a lost race; the precondition held")
	}
	metadataCASAssertCurrent(t, result, `"same"`)

	absent := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "never.written",
	})
	if !absent.Swapped || absent.Current != nil {
		t.Fatalf("absent-to-absent = %+v, want Swapped with a nil Current", absent)
	}
	if after := metadataCASUpdatedAt(t, ctx, fixture, id); after != before {
		t.Errorf("updated_at moved from %q to %q over two swaps that changed nothing", before, after)
	}
}

// RunMetadataCASRefusesAnIDOnNeitherPlane pins ErrNotFound for an id that names
// no row. It is a refusal rather than a lost race because a caller's retry loop
// can never converge on a missing issue.
func RunMetadataCASRefusesAnIDOnNeitherPlane(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	result, err := fixture.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
		Actor:   "cas-tester",
		IssueID: fixture.IssuePrefix + "-ghost-9999",
		Key:     "gc.lease",
		Value:   metadataCASValue(`"holder"`),
	})
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("CompareAndSetKey on an absent id error = %v, want ErrNotFound", err)
	}
	if result.Swapped {
		t.Error("a refused swap reported Swapped")
	}
}

// RunMetadataCASRefusesAnUnusableRequest pins that the request rules are the
// ROLE's rather than a front door's, so a second caller inherits them. The rule
// definitions are pinned without a database beside the plan function; what this
// case adds is that every implementation actually runs them, and refuses BEFORE
// it touches a row.
func RunMetadataCASRefusesAnUnusableRequest(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "refuse", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"kept"`),
	})

	for _, test := range []struct {
		name    string
		request publicops.CompareAndSetKeyRequest
	}{
		{"empty actor", publicops.CompareAndSetKeyRequest{IssueID: id, Key: "phase"}},
		{"empty issue id", publicops.CompareAndSetKeyRequest{Actor: "cas-tester", Key: "phase"}},
		{"empty key", publicops.CompareAndSetKeyRequest{Actor: "cas-tester", IssueID: id}},
		{"key outside the syntax", publicops.CompareAndSetKeyRequest{
			Actor: "cas-tester", IssueID: id, Key: "9 bad key"}},
		{"malformed expected", publicops.CompareAndSetKeyRequest{
			Actor: "cas-tester", IssueID: id, Key: "phase", Expected: metadataCASValue(`{`)}},
		{"malformed value", publicops.CompareAndSetKeyRequest{
			Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`nope`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := fixture.MetadataCAS.CompareAndSetKey(ctx, test.request)
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("CompareAndSetKey(%s) error = %v, want ErrValidation", test.name, err)
			}
			if result.Swapped {
				t.Error("a refused request reported Swapped")
			}
		})
	}
	metadataCASAssertStored(t, ctx, fixture, id, "phase", `"kept"`)
}

// RunMetadataCASResolvesAWispAnchor pins that the id resolves across BOTH
// planes without the caller saying which (CompareAndSetKeyRequest.IssueID), the
// way Reader.Get and Commenter.AddComment do.
func RunMetadataCASResolvesAWispAnchor(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "wisp", true)

	result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"ephemeral"`),
	})
	if !result.Swapped {
		t.Fatal("a swap on a wisp was refused; the id resolves across both planes")
	}
	metadataCASAssertStoredIn(t, ctx, fixture, "wisps", id, "phase", `"ephemeral"`)

	if lost, err := fixture.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"again"`),
	}); err != nil || lost.Swapped {
		t.Fatalf("a second create on the wisp = (%+v, %v), want a refused swap and no error", lost, err)
	}
}

// RunMetadataCASAWispSwapRecordsNoDurableHistory pins the ephemeral clause of
// MetadataCAS.CompareAndSetKey: a swap on a wisp records NO durable history
// entry — none, not "at most one" — because the wisp tables are ignored by the
// version-control plane and an entry naming one would be the sync artifact that
// ignoring them exists to prevent.
//
// It is a case of its own rather than a line in the wisp-resolution one because
// it is a different promise, and because the promise was previously held by TWO
// LAYERS COINCIDENTALLY AGREEING — the write routes its event to wisp_events,
// and ChangedTables.Add drops the wisp tables — with nothing asserting either.
//
// THE DURABLE-EVENT COUNT IS UNCONDITIONAL and the version delta is not: the
// raw count is a fact about a table every backend has, so it pins the promise
// even where history cannot be observed.
func RunMetadataCASAWispSwapRecordsNoDurableHistory(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "wisphistory", true)

	settled := fixture.CountHistory != nil && fixture.CommitPending != nil
	before := 0
	if settled {
		if err := fixture.CommitPending(ctx); err != nil {
			t.Fatalf("settling the seeds: %v", err)
		}
		before = metadataCASHistory(t, ctx, fixture)
	}

	if result := metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"ephemeral"`),
	}); !result.Swapped {
		t.Fatal("the swap on the wisp was refused")
	}
	metadataCASAssertStoredIn(t, ctx, fixture, "wisps", id, "phase", `"ephemeral"`)

	var durable int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", []any{id}, &durable); err != nil {
		t.Fatalf("counting durable events for %s: %v", id, err)
	}
	if durable != 0 {
		t.Errorf("a swap on a wisp wrote %d row(s) into the DURABLE events table, want none: "+
			"an entry naming an ephemeral row is the sync artifact the ignored tables exist to prevent", durable)
	}
	if settled {
		if got := metadataCASHistory(t, ctx, fixture) - before; got != 0 {
			t.Errorf("a swap on a wisp recorded %d version-control entries, want none", got)
		}
	}
}

// RunMetadataCASRecordsExactlyOneHistoryEntry pins the version-control clause:
// a swap that MOVED the value records one entry, and neither a lost race nor a
// value-to-itself swap records any.
//
// The deltas are taken around each call, and the seeds are settled first, for
// the reason SweeperFixture.CommitPending gives: a swap of rows that never
// reached the history is not a change against the history on every backend.
func RunMetadataCASRecordsExactlyOneHistoryEntry(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the entry-per-swap clause is unobservable here")
	}
	if fixture.CommitPending == nil {
		t.Skip("this backend cannot settle its history on demand, so a swap over these seeds is not a change against the history")
	}
	id := metadataCASSeedIssue(t, ctx, fixture, "history", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"one"`),
	})
	if err := fixture.CommitPending(ctx); err != nil {
		t.Fatalf("settling the seeds: %v", err)
	}

	before := metadataCASHistory(t, ctx, fixture)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase",
		Expected: metadataCASValue(`"one"`), Value: metadataCASValue(`"two"`),
	})
	if got := metadataCASHistory(t, ctx, fixture) - before; got != 1 {
		t.Errorf("a swap that moved the value recorded %d history entries, want exactly 1", got)
	}

	before = metadataCASHistory(t, ctx, fixture)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase",
		Expected: metadataCASValue(`"two"`), Value: metadataCASValue(`"two"`),
	})
	if got := metadataCASHistory(t, ctx, fixture) - before; got != 0 {
		t.Errorf("a swap over an already-equal value recorded %d history entries, want none", got)
	}
}

// RunMetadataCASHistoryEntryNamesTheActor is why Actor is REQUIRED on this
// request (CompareAndSetKeyRequest.Actor): a swap is a coordination write
// between racing callers, and the one question asked of its trace afterwards is
// which of them won.
//
// Nothing else holds it. Every other case would pass with the actor dropped on
// the floor between the role and the row, because no result member carries it.
func RunMetadataCASHistoryEntryNamesTheActor(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "actor", false)

	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-winner", IssueID: id, Key: "gc.lease", Value: metadataCASValue(`"holder-a"`),
	})
	// A SECOND actor, so the case cannot pass on a body that stamps a constant
	// — the seeding actor, the store's identity, or the first swap's.
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-successor", IssueID: id, Key: "gc.lease",
		Expected: metadataCASValue(`"holder-a"`), Value: metadataCASValue(`"holder-b"`),
	})

	// COUNTED PER ACTOR, NOT READ OFF THE NEWEST ROW, and the difference is what
	// makes this case able to fail for the right reason. created_at is
	// second-granularity, so two swaps in one test share a timestamp and no
	// tie-break orders them — an ORDER BY here picks arbitrarily between the two
	// and the case decides its verdict on a coin toss. It was written that way
	// first and failed on exactly that.
	for _, actor := range []string{"cas-winner", "cas-successor"} {
		if got := metadataCASEventsByActor(t, ctx, fixture, id, actor); got != 1 {
			t.Errorf("%s has %d event(s) attributed to %q, want exactly 1: "+
				"the actor a swap is asked for is the actor its trace must name", id, got, actor)
		}
	}
}

// RunMetadataCASARefusedSwapRecordsNoHistory pins the other half of the
// version-control clause, and the half a mutation is most likely to break: a
// body that opened a committing transaction before it compared would record an
// entry for a race it lost.
func RunMetadataCASARefusedSwapRecordsNoHistory(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so the no-entry-for-a-refusal clause is unobservable here")
	}
	if fixture.CommitPending == nil {
		t.Skip("this backend cannot settle its history on demand, so a refusal is not measurable against the history")
	}
	id := metadataCASSeedIssue(t, ctx, fixture, "norecord", false)
	metadataCASSwap(t, ctx, fixture, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase", Value: metadataCASValue(`"held"`),
	})
	if err := fixture.CommitPending(ctx); err != nil {
		t.Fatalf("settling the seeds: %v", err)
	}

	before := metadataCASHistory(t, ctx, fixture)
	if _, err := fixture.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "phase",
		Expected: metadataCASValue(`"stale"`), Value: metadataCASValue(`"clobbered"`),
	}); err != nil {
		t.Fatalf("a lost race returned error = %v, want nil", err)
	}
	if got := metadataCASHistory(t, ctx, fixture) - before; got != 0 {
		t.Errorf("a lost race recorded %d history entries, want none", got)
	}

	if _, err := fixture.MetadataCAS.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "9 bad key",
	}); !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("a malformed request error = %v, want ErrValidation", err)
	}
	if got := metadataCASHistory(t, ctx, fixture) - before; got != 0 {
		t.Errorf("a refused request recorded %d history entries, want none", got)
	}
}

// RunMetadataCASDoesNotMutateTheCallerRequest pins the no-mutation promise on
// the role whose request is mostly POINTERS: the two raw values are read, never
// written through, and canonicalization happens on a copy.
func RunMetadataCASDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture MetadataCASFixture) {
	t.Helper()
	id := metadataCASSeedIssue(t, ctx, fixture, "immutable", false)

	expected := json.RawMessage("{ \"b\":2,\n\"a\":1 }")
	value := json.RawMessage(`{ "z" : true }`)
	request := publicops.CompareAndSetKeyRequest{
		Actor: "cas-tester", IssueID: id, Key: "shape", Expected: &expected, Value: &value,
	}
	snapshot := request
	expectedBytes := string(expected)
	valueBytes := string(value)

	if _, err := fixture.MetadataCAS.CompareAndSetKey(ctx, request); err != nil {
		t.Fatalf("CompareAndSetKey error = %v", err)
	}
	if !reflect.DeepEqual(request, snapshot) {
		t.Errorf("the request was rewritten: %+v, want %+v", request, snapshot)
	}
	if string(expected) != expectedBytes {
		t.Errorf("Expected was rewritten through the pointer: %s, want %s", expected, expectedBytes)
	}
	if string(value) != valueBytes {
		t.Errorf("Value was rewritten through the pointer: %s, want %s", value, valueBytes)
	}
}

// --- fixture helpers -------------------------------------------------------

// metadataCASValue is the request pointer's constructor. It exists because the
// two optional values are pointers to raw JSON, and every case would otherwise
// spend two lines minting one.
func metadataCASValue(raw string) *json.RawMessage {
	value := json.RawMessage(raw)
	return &value
}

// metadataCASSeedIssue writes one issue through the plane its ephemeral flag
// names and returns its id. Each case gets its own id, so each gets its own key
// space.
func metadataCASSeedIssue(t *testing.T, ctx context.Context, fixture MetadataCASFixture, tag string, ephemeral bool) string {
	t.Helper()
	issue := &types.Issue{
		ID:        fmt.Sprintf("%s-cas-%s", fixture.IssuePrefix, tag),
		Title:     "metadata cas " + tag,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: ephemeral,
	}
	create := fixture.CreateIssue
	if ephemeral {
		create = fixture.CreateWisp
	}
	if err := create(ctx, issue, "cas-seed"); err != nil {
		t.Fatalf("seeding %s: %v", issue.ID, err)
	}
	return issue.ID
}

// metadataCASSwap runs a swap the case expects to complete, and fatals on an
// error. It does NOT assert the verdict: a refused swap completes.
func metadataCASSwap(t *testing.T, ctx context.Context, fixture MetadataCASFixture, request publicops.CompareAndSetKeyRequest) publicops.CompareAndSetKeyResult {
	t.Helper()
	result, err := fixture.MetadataCAS.CompareAndSetKey(ctx, request)
	if err != nil {
		t.Fatalf("CompareAndSetKey(%s on %s) error = %v", request.Key, request.IssueID, err)
	}
	return result
}

// metadataCASAssertCurrent compares the reported current value against the
// canonical encoding the role promises.
func metadataCASAssertCurrent(t *testing.T, result publicops.CompareAndSetKeyResult, want string) {
	t.Helper()
	if result.Current == nil {
		t.Fatalf("Current = nil, want %s", want)
	}
	if got := string(*result.Current); got != want {
		t.Errorf("Current = %s, want %s (the canonical encoding)", got, want)
	}
}

// metadataCASAssertStored reads the metadata COLUMN and compares one key's
// stored value. It is the assertion that does not trust what the role reported
// about itself.
func metadataCASAssertStored(t *testing.T, ctx context.Context, fixture MetadataCASFixture, id, key, want string) {
	t.Helper()
	metadataCASAssertStoredIn(t, ctx, fixture, "issues", id, key, want)
}

func metadataCASAssertStoredIn(t *testing.T, ctx context.Context, fixture MetadataCASFixture, table, id, key, want string) {
	t.Helper()
	stored, ok := metadataCASReadKey(t, ctx, fixture, table, id, key)
	if !ok {
		t.Fatalf("%s.metadata on %s has no key %q, want %s", table, id, key, want)
	}
	got, err := canonicalizeForContract(stored)
	if err != nil {
		t.Fatalf("stored value for %s.%s is not JSON: %v", id, key, err)
	}
	if got != want {
		t.Errorf("stored %s.%s = %s, want %s", id, key, got, want)
	}
}

func metadataCASAssertAbsent(t *testing.T, ctx context.Context, fixture MetadataCASFixture, id, key string) {
	t.Helper()
	if stored, ok := metadataCASReadKey(t, ctx, fixture, "issues", id, key); ok {
		t.Errorf("metadata on %s still carries %q = %s, want the key removed", id, key, stored)
	}
}

// metadataCASReadKey reads the whole metadata blob off the row and reports one
// key's raw bytes. The WHOLE BLOB is read rather than a JSON path extract,
// because that is what lets the caller see an absent key and a null one as the
// different things they are — a path extract answers NULL for both.
func metadataCASReadKey(t *testing.T, ctx context.Context, fixture MetadataCASFixture, table, id, key string) (json.RawMessage, bool) {
	t.Helper()
	// Scanned as a STRING, which is the destination the three kits' scalar
	// readers agree on; a []byte destination is one of them only.
	var blob string
	query := fmt.Sprintf("SELECT COALESCE(metadata, '{}') FROM %s WHERE id = ?", table)
	if err := fixture.QueryScalar(ctx, query, []any{id}, &blob); err != nil {
		t.Fatalf("reading %s.metadata for %s: %v", table, id, err)
	}
	if blob == "" || blob == "null" {
		return nil, false
	}
	object := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(blob), &object); err != nil {
		t.Fatalf("parsing %s.metadata for %s (%s): %v", table, id, blob, err)
	}
	stored, ok := object[key]
	return stored, ok
}

// metadataCASUpdatedAt reads the row's updated_at as text, for the case that
// asserts a swap which changed nothing touched nothing.
func metadataCASUpdatedAt(t *testing.T, ctx context.Context, fixture MetadataCASFixture, id string) string {
	t.Helper()
	var stamp string
	if err := fixture.QueryScalar(ctx, "SELECT CAST(updated_at AS CHAR) FROM issues WHERE id = ?", []any{id}, &stamp); err != nil {
		t.Fatalf("reading updated_at for %s: %v", id, err)
	}
	return stamp
}

// metadataCASEventsByActor counts the durable events on an issue attributed to
// one actor. It goes through the frozen kit's scalar seam rather than a new
// fixture hook, because the events table is one every backend of this contract
// already has and the kit already publishes a way to read it.
func metadataCASEventsByActor(t *testing.T, ctx context.Context, fixture MetadataCASFixture, id, actor string) int {
	t.Helper()
	var count int
	const query = "SELECT COUNT(*) FROM events WHERE issue_id = ? AND actor = ?"
	if err := fixture.QueryScalar(ctx, query, []any{id, actor}, &count); err != nil {
		t.Fatalf("counting events for %s attributed to %q: %v", id, actor, err)
	}
	return count
}

func metadataCASHistory(t *testing.T, ctx context.Context, fixture MetadataCASFixture) int {
	t.Helper()
	count, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory: %v", err)
	}
	return count
}

// canonicalizeForContract re-encodes a stored value the way the role's equality
// rule does, so a case can compare against one spelling of the value rather
// than against whatever bytes a backend's JSON column handed back.
//
// It is a LOCAL re-implementation rather than a call into the storage package,
// which this package deliberately does not import: a conformance contract that
// asserted through the implementation's own canonicalizer would go green on a
// canonicalizer that changed. Object key order is the only thing it has to
// settle, since the values these cases store are small.
//
// IT HAS A BLIND SPOT OF ITS OWN, stated here so no case is written in
// ignorance of it: decoding into `any` puts every number through float64, so
// this helper cannot tell 1 from 1.0 or two integers that differ past 2^53
// apart. No case may assert a NUMERIC value through it — the one case about
// numbers reads the raw column bytes instead
// (RunMetadataCASReportsTheValueTheRowHolds).
func canonicalizeForContract(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("empty value")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
