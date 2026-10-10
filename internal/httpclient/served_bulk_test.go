//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_bulk_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Sweeper, Deleter and BatchCreator contracts against the served surface.
//
// The three roles sit at very different distances from the wire, and the parks
// below say where each one stops:
//
//   - SWEEP maps whole, in both directions. Every case runs.
//   - DELETE maps its whole REQUEST and diverges only in its refusal
//     VOCABULARY: the wire flattens *NotFoundError and
//     *DependentsOutsideRequestError into a bare sentinel and a 400. Four cases
//     bind those typed errors and park; the behavior underneath them — the
//     request fails and nothing is deleted — is asserted for real by the two
//     bespoke cases below, so the rows retire loudly rather than sitting behind
//     a skip.
//   - BATCH CREATE maps eight content members of a whole types.Issue, and every
//     case that names an explicit id parks with it. That is most of the family,
//     because the contract's own preamble says why: "Every case names its own
//     ids under the fixture prefix and passes ForceIDPrefix." Three bespoke
//     cases below cover the same ground with server-generated ids.
//
// The bead that owns every park here, and unparks each one as the wire grows
// the member behind it.
const bulkParkBead = "ga-6lbny"

// ── Sweeper ─────────────────────────────────────────────────────────────────

func newServedSweeperFixture(t *testing.T, prefix string) conformance.SweeperFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	sweeper, err := env.subject.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}
	return conformance.SweeperFixture{
		IssuePrefix:   env.prefix,
		Sweeper:       sweeper,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
		CommitPending: env.commitPending,
		AddComment:    env.addComment,
		// Exec is the harness's own raw door, the one the close/reopen cases
		// already use. The tier-boundary case needs a LEGACY typed wisp — issues
		// plane, wisp_type set, ephemeral 0 — and no create verb can mint one,
		// on this leg least of all: the wire has no raw-row surface, so the
		// served store reaches the shape through the embedded database behind
		// the server and then exercises the sweep over the wire as usual.
		Exec: env.exec,
		AddDependencies: func(ctx context.Context, req issueops.AddDependenciesRequest) error {
			// Through the DependencyEditor ROLE, which routes each edge to its
			// source plane's dependency table itself — the same seam
			// dolt/uow/embeddeddolt's own sweeper fixtures use.
			editor, err := env.subject.DependencyEditor()
			if err != nil {
				return err
			}
			_, err = editor.AddDependencies(ctx, req)
			return err
		},
	}
}

func TestServedSweeperRefusesAnUnfilteredDurableSweep(t *testing.T) {
	conformance.RunSweeperRefusesAnUnfilteredDurableSweep(t, t.Context(), newServedSweeperFixture(t, "hs00"))
}

func TestServedSweeperRefusesAMalformedRequest(t *testing.T) {
	conformance.RunSweeperRefusesAMalformedRequest(t, t.Context(), newServedSweeperFixture(t, "hs01"))
}

func TestServedSweeperClearsOneTierAndLeavesTheOther(t *testing.T) {
	conformance.RunSweeperClearsOneTierAndLeavesTheOther(t, t.Context(), newServedSweeperFixture(t, "hs02"))
}

func TestServedSweeperTreatsALegacyTypedWispAsEphemeralTier(t *testing.T) {
	conformance.RunSweeperTreatsALegacyTypedWispAsEphemeralTier(t, t.Context(), newServedSweeperFixture(t, "hs11"))
}

func TestServedSweeperLeavesNoHistoryBeadsToTheDurableTier(t *testing.T) {
	conformance.RunSweeperLeavesNoHistoryBeadsToTheDurableTier(t, t.Context(), newServedSweeperFixture(t, "hs12"))
}

func TestServedSweeperProtectsPinnedRows(t *testing.T) {
	conformance.RunSweeperProtectsPinnedRows(t, t.Context(), newServedSweeperFixture(t, "hs03"))
}

func TestServedSweeperHonorsTheCutoffAndThePattern(t *testing.T) {
	conformance.RunSweeperHonorsTheCutoffAndThePattern(t, t.Context(), newServedSweeperFixture(t, "hs04"))
}

func TestServedSweeperDryRunChangesNothing(t *testing.T) {
	conformance.RunSweeperDryRunChangesNothing(t, t.Context(), newServedSweeperFixture(t, "hs05"))
}

func TestServedSweeperProtectsRowsCitedFromAWispComment(t *testing.T) {
	conformance.RunSweeperProtectsRowsCitedFromAWispComment(t, t.Context(), newServedSweeperFixture(t, "hs06"))
}

func TestServedSweeperProtectsCitedRows(t *testing.T) {
	conformance.RunSweeperProtectsCitedRows(t, t.Context(), newServedSweeperFixture(t, "hs07"))
}

func TestServedSweeperEmptyMatchIsZeroAndNil(t *testing.T) {
	conformance.RunSweeperEmptyMatchIsZeroAndNil(t, t.Context(), newServedSweeperFixture(t, "hs08"))
}

func TestServedSweeperRecordsExactlyOneHistoryEntry(t *testing.T) {
	conformance.RunSweeperRecordsExactlyOneHistoryEntry(t, t.Context(), newServedSweeperFixture(t, "hs09"))
}

func TestServedSweeperDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunSweeperDoesNotMutateTheCallerRequest(t, t.Context(), newServedSweeperFixture(t, "hs10"))
}

// The S4 wire extension: tier: "wisps-plane", ProtectLiveDependents and Limit
// all now have a round trip (internal/httpapi/sweep.go, internal/httpclient/
// sweeper.go), each behind its own capability token the served harness's
// in-process handshake always advertises, so these six run for real rather
// than parking.

func TestServedSweeperWispsPlaneClearsTheWholeWispsTable(t *testing.T) {
	conformance.RunSweeperWispsPlaneClearsTheWholeWispsTable(t, t.Context(), newServedSweeperFixture(t, "hs13"))
}

func TestServedSweeperWispsPlaneRequiresAFilter(t *testing.T) {
	conformance.RunSweeperWispsPlaneRequiresAFilter(t, t.Context(), newServedSweeperFixture(t, "hs14"))
}

func TestServedSweeperProtectsLiveDependents(t *testing.T) {
	conformance.RunSweeperProtectsLiveDependents(t, t.Context(), newServedSweeperFixture(t, "hs15"))
}

func TestServedSweeperProtectsTransitiveLiveDependents(t *testing.T) {
	conformance.RunSweeperProtectsTransitiveLiveDependents(t, t.Context(), newServedSweeperFixture(t, "hs16"))
}

func TestServedSweeperProtectsLiveDependentsAcrossPlanes(t *testing.T) {
	conformance.RunSweeperProtectsLiveDependentsAcrossPlanes(t, t.Context(), newServedSweeperFixture(t, "hs17"))
}

func TestServedSweeperLimitTakesTheOldestClosedFirst(t *testing.T) {
	conformance.RunSweeperLimitTakesTheOldestClosedFirst(t, t.Context(), newServedSweeperFixture(t, "hs18"))
}

// ── Deleter ─────────────────────────────────────────────────────────────────

func newServedDeleterFixture(t *testing.T, prefix string) conformance.DeleterFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	deleter, err := env.subject.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}
	return conformance.DeleterFixture{
		IssuePrefix:   env.prefix,
		Deleter:       deleter,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		AddDependency: env.addDependency,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
		CommitPending: env.commitPending,
	}
}

func TestServedDeleterRefusesAMalformedRequest(t *testing.T) {
	conformance.RunDeleterRefusesAMalformedRequest(t, t.Context(), newServedDeleterFixture(t, "hx00"))
}

func TestServedDeleterRefusesAnAbsentID(t *testing.T) {
	skipKnownDivergence(t, "L-delete-notfound", bulkParkBead, parkDeleteNotFound)
	conformance.RunDeleterRefusesAnAbsentID(t, t.Context(), newServedDeleterFixture(t, "hx01"))
}

func TestServedDeleterRefusesDependentsOutsideTheRequest(t *testing.T) {
	skipKnownDivergence(t, "L-delete-dependents", bulkParkBead, parkDeleteDependents)
	conformance.RunDeleterRefusesDependentsOutsideTheRequest(t, t.Context(), newServedDeleterFixture(t, "hx02"))
}

func TestServedDeleterForceOrphansDependents(t *testing.T) {
	conformance.RunDeleterForceOrphansDependents(t, t.Context(), newServedDeleterFixture(t, "hx03"))
}

func TestServedDeleterCascadeDeletesTheClosure(t *testing.T) {
	conformance.RunDeleterCascadeDeletesTheClosure(t, t.Context(), newServedDeleterFixture(t, "hx04"))
}

func TestServedDeleterCascadeFromAWispRootDeletesTheClosure(t *testing.T) {
	conformance.RunDeleterCascadeFromAWispRootDeletesTheClosure(t, t.Context(), newServedDeleterFixture(t, "hx05"))
}

func TestServedDeleterGuardsAWispNamedWithADurableDependent(t *testing.T) {
	skipKnownDivergence(t, "L-delete-dependents", bulkParkBead, parkDeleteDependents)
	conformance.RunDeleterGuardsAWispNamedWithADurableDependent(t, t.Context(), newServedDeleterFixture(t, "hx06"))
}

func TestServedDeleterGuardsADurableNamedWithAWispDependent(t *testing.T) {
	skipKnownDivergence(t, "L-delete-dependents", bulkParkBead, parkDeleteDependents)
	conformance.RunDeleterGuardsADurableNamedWithAWispDependent(t, t.Context(), newServedDeleterFixture(t, "hx07"))
}

func TestServedDeleterCountsCrossPlaneEdgesItRemoves(t *testing.T) {
	conformance.RunDeleterCountsCrossPlaneEdgesItRemoves(t, t.Context(), newServedDeleterFixture(t, "hx08"))
}

func TestServedDeleterNeverCallsALiveRowDeleted(t *testing.T) {
	conformance.RunDeleterNeverCallsALiveRowDeleted(t, t.Context(), newServedDeleterFixture(t, "hx09"))
}

func TestServedDeleterErasesAcrossBothPlanes(t *testing.T) {
	conformance.RunDeleterErasesAcrossBothPlanes(t, t.Context(), newServedDeleterFixture(t, "hx10"))
}

func TestServedDeleterCollapsesDuplicateIDs(t *testing.T) {
	conformance.RunDeleterCollapsesDuplicateIDs(t, t.Context(), newServedDeleterFixture(t, "hx11"))
}

func TestServedDeleterRewritesReferencesInNeighbors(t *testing.T) {
	conformance.RunDeleterRewritesReferencesInNeighbors(t, t.Context(), newServedDeleterFixture(t, "hx12"))
}

func TestServedDeleterRewritesOnlyTheDeletedIDInAHierarchy(t *testing.T) {
	conformance.RunDeleterRewritesOnlyTheDeletedIDInAHierarchy(t, t.Context(), newServedDeleterFixture(t, "hx23"))
}

func TestServedDeleterDryRunChangesNothing(t *testing.T) {
	conformance.RunDeleterDryRunChangesNothing(t, t.Context(), newServedDeleterFixture(t, "hx13"))
}

func TestServedDeleterRecordsExactlyOneHistoryEntry(t *testing.T) {
	conformance.RunDeleterRecordsExactlyOneHistoryEntry(t, t.Context(), newServedDeleterFixture(t, "hx14"))
}

func TestServedDeleterDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunDeleterDoesNotMutateTheCallerRequest(t, t.Context(), newServedDeleterFixture(t, "hx15"))
}

// The compare-and-delete precondition, end to end. It is the guard whose failure
// mode has no undo — a dropped one erases a row the caller asked not to erase,
// with nothing left to compare afterwards — so all four of its contracts run
// against a real server rather than stopping at the request assertion.
//
// The arity case is the one that could not have been anticipated client-side:
// the wire refuses a guard beside more than one DISTINCT id, and distinctness
// there is measured after trimming and collapsing duplicates, so `["x", "  x  "]`
// is ONE bead and legal. That normalization belongs to the server, and this is
// where the client's verbatim ids meet it.

func TestServedDeleterDeletesOnAMatchingExpectedVersion(t *testing.T) {
	conformance.RunDeleterDeletesOnAMatchingExpectedVersion(t, t.Context(), newServedDeleterFixture(t, "hx16"))
}

func TestServedDeleterRefusesAStaleExpectedVersion(t *testing.T) {
	conformance.RunDeleterRefusesAStaleExpectedVersion(t, t.Context(), newServedDeleterFixture(t, "hx17"))
}

func TestServedDeleterVersionOutranksForceAndCascade(t *testing.T) {
	conformance.RunDeleterVersionOutranksForceAndCascade(t, t.Context(), newServedDeleterFixture(t, "hx18"))
}

func TestServedDeleterRefusesAnExpectedVersionAcrossSeveralIDs(t *testing.T) {
	conformance.RunDeleterRefusesAnExpectedVersionAcrossSeveralIDs(t, t.Context(), newServedDeleterFixture(t, "hx19"))
}

// The three BLOCKED-STATE postconditions of a delete, which map onto the wire
// whole: the request is expressible, and what they assert is what the server's
// deleting transaction leaves behind in a column no read on this surface
// hydrates.
//
// They matter most on the SURVIVORS. A delete is the one write that removes the
// blocker rather than closing it, so a body that settled by re-reading the
// deleted row would find nothing and leave every depender marked blocked by an
// id that no longer exists — a row that can never become ready again and that
// no listing can explain.
func TestServedDeleterSettlesTheSurvivorsOfADeletedBlocker(t *testing.T) {
	conformance.RunDeleterSettlesTheSurvivorsOfADeletedBlocker(t, t.Context(), newServedDeleterFixture(t, "hx20"))
}

func TestServedDeleterSettlesTheSurvivorsOfADeletedWispBlocker(t *testing.T) {
	conformance.RunDeleterSettlesTheSurvivorsOfADeletedWispBlocker(t, t.Context(), newServedDeleterFixture(t, "hx21"))
}

// TestServedDeleterSettlesTheChildrenOfADeletedParent is the inherited half: a
// child orphaned from a blocked parent inherits nothing, so the same
// transaction has to unmark it.
func TestServedDeleterSettlesTheChildrenOfADeletedParent(t *testing.T) {
	conformance.RunDeleterSettlesTheChildrenOfADeletedParent(t, t.Context(), newServedDeleterFixture(t, "hx22"))
}

const parkDeleteNotFound = "the case binds *NotFoundError.IDs, and the wire's 404 carries a FIXED detail that cannot echo caller input back, so the ids that did not resolve are unrecoverable (asserted by TestServedDeleteRefusesAnAbsentIDWithoutNamingIt)"

const parkDeleteDependents = "the case binds *DependentsOutsideRequestError, and the wire flattens the unforced dependents guard into a 400 invalid_argument with no param, so the blocked id and its dependents cannot be reconstructed without parsing detail prose. " +
	"The GUARD is asserted for all three quadrants the contract covers: TestServedDeleteDependentsGuardRefusesAsValidation (durable named, durable dependent, plus the both-ends-named exception), " +
	"TestServedDeleteGuardsAWispNamedWithADurableDependent and TestServedDeleteGuardsADurableNamedWithAWispDependent (the two cross-plane directions, unforced refusal AND forced orphan report)"

// TestServedDeleteRefusesAnAbsentIDWithoutNamingIt is L-delete-notfound's pin,
// and it RUNS.
//
// The row is a divergence in CLASSIFICATION, not in outcome, and the outcome is
// the half that matters: an id naming no row fails the whole request and
// deletes NOTHING — not even the id beside it that did resolve. Asserting the
// absence of the typed error is what makes the row retire the day the wire
// grows an ids extension member.
func TestServedDeleteRefusesAnAbsentIDWithoutNamingIt(t *testing.T) {
	env := newServedEnv(t, "hxnf")
	ctx := t.Context()
	deleter, err := env.subject.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}

	const stored = "hxnf-real"
	seedServedIssue(t, ctx, env, stored, types.StatusOpen)

	for _, dryRun := range []bool{false, true} {
		result, err := deleter.Delete(ctx, issueops.DeleteRequest{
			IDs: []string{stored, "hxnf-nosuchrow"}, Force: true, DryRun: dryRun,
		})
		if !errors.Is(err, issueops.ErrNotFound) {
			t.Fatalf("Delete(dryRun=%v) error = %v, want ErrNotFound", dryRun, err)
		}
		var notFound *issueops.NotFoundError
		if errors.As(err, &notFound) {
			t.Errorf("the refusal carries *NotFoundError(%v); L-delete-notfound says the wire cannot express it — retire the row rather than the assertion", notFound.IDs)
		}
		if result.Deleted != 0 {
			t.Errorf("refused delete reported Deleted = %d, want 0", result.Deleted)
		}
		assertServedIssueRows(t, ctx, env, 1, stored)
	}
}

// TestServedDeleteDependentsGuardRefusesAsValidation is L-delete-dependents'
// pin, and it RUNS. The guard itself is untouched: the request fails and the
// graph is whole.
func TestServedDeleteDependentsGuardRefusesAsValidation(t *testing.T) {
	env := newServedEnv(t, "hxdg")
	ctx := t.Context()
	deleter, err := env.subject.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}

	const blocker, dependent = "hxdg-blocker", "hxdg-dependent"
	seedServedIssue(t, ctx, env, blocker, types.StatusOpen)
	seedServedIssue(t, ctx, env, dependent, types.StatusOpen)
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the edge: %v", err)
	}

	result, err := deleter.Delete(ctx, issueops.DeleteRequest{IDs: []string{blocker}})
	if err == nil {
		t.Fatal("an unforced delete over an outside dependent succeeded; the guard is the role's and must survive the crossing")
	}
	if !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("refusal = %v, want ErrValidation — the wire spells this as a 400 invalid_argument", err)
	}
	if errors.Is(err, issueops.ErrDependentsOutsideRequest) {
		t.Error("the refusal carries ErrDependentsOutsideRequest; L-delete-dependents says the wire cannot express it — retire the row rather than the assertion")
	}
	if result.Deleted != 0 {
		t.Errorf("refused delete reported Deleted = %d, want 0", result.Deleted)
	}
	assertServedIssueRows(t, ctx, env, 1, blocker, dependent)

	// The EXCEPTION beside the guard, which the wire carries whole: a dependent
	// INSIDE the request is not a dependent for this purpose. Refusing it would
	// make `delete a b` fail on exactly the pair a caller took care to list
	// together, and it is the one arm a body that partitioned the request by
	// plane could still get wrong.
	both, err := deleter.Delete(ctx, issueops.DeleteRequest{IDs: []string{blocker, dependent}})
	if err != nil {
		t.Fatalf("Delete() naming both ends of the edge: %v — a dependent INSIDE the request is not a dependent", err)
	}
	if both.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2", both.Deleted)
	}
	assertServedIssueRows(t, ctx, env, 0, blocker, dependent)
}

// TestServedDeleteGuardsAWispNamedWithADurableDependent and its sibling below
// are the two CROSS-PLANE quadrants of the dependents guard, over the wire.
//
// They exist because the contract cases that cover them park on the typed
// error, and parking all four quadrants on one durable-to-durable assertion
// would leave exactly the hole deleter_contract.go says it was written to
// close: a body that asked the guard about the durable half of the request only
// "orphaned a durable dependent without saying so and without being refused".
// The guard is the SERVER's here, but a client that partitioned a mixed request
// by plane would reintroduce it below the wire, and nothing else would see it.
//
// Both halves of the clause are asserted, because a body can get either one
// right on its own: the unforced refusal leaves both rows AND the edge, and the
// forced run deletes exactly the named row and NAMES the cross-plane orphan.
func TestServedDeleteGuardsAWispNamedWithADurableDependent(t *testing.T) {
	env := newServedEnv(t, "hxwg")
	ctx := t.Context()
	deleter, err := env.subject.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}

	// The NAMED row is the wisp; its dependent is durable, so the edge lives in
	// `dependencies` with the wisp as the target.
	const wisp, dependent = "hxwg-wisp", "hxwg-dependent"
	seedServedWisp(t, ctx, env, wisp)
	seedServedIssue(t, ctx, env, dependent, types.StatusOpen)
	seedServedEdge(t, ctx, env, dependent, wisp)
	assertServedEdgeRows(t, ctx, env, "dependencies", 1, dependent, wisp)

	for _, dryRun := range []bool{false, true} {
		result, err := deleter.Delete(ctx, issueops.DeleteRequest{IDs: []string{wisp}, DryRun: dryRun})
		assertServedDependentsRefusal(t, err, dryRun)
		if result.Deleted != 0 {
			t.Errorf("refused delete (dryRun=%v) reported Deleted = %d, want 0", dryRun, result.Deleted)
		}
		assertServedWispRows(t, ctx, env, 1, wisp)
		assertServedIssueRows(t, ctx, env, 1, dependent)
		assertServedEdgeRows(t, ctx, env, "dependencies", 1, dependent, wisp)
	}

	forced, err := deleter.Delete(ctx, issueops.DeleteRequest{IDs: []string{wisp}, Force: true})
	if err != nil {
		t.Fatalf("forced delete of the wisp: %v", err)
	}
	if forced.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 — force deletes the NAMED wisp and nothing else", forced.Deleted)
	}
	if !reflect.DeepEqual(forced.Orphaned, []string{dependent}) {
		t.Errorf("Orphaned = %v, want [%s] — the cross-plane edge orphans a durable row too", forced.Orphaned, dependent)
	}
	assertServedWispRows(t, ctx, env, 0, wisp)
	assertServedIssueRows(t, ctx, env, 1, dependent)
	assertServedEdgeRows(t, ctx, env, "dependencies", 0, dependent, wisp)
}

// TestServedDeleteGuardsADurableNamedWithAWispDependent is the fourth quadrant:
// the WISP is the dependent, so the edge lands in wisp_dependencies, and the
// durable row it depends on is the one named unforced. The two planes are
// scanned by different queries below the wire, so this is a genuinely separate
// answer from the case above.
func TestServedDeleteGuardsADurableNamedWithAWispDependent(t *testing.T) {
	env := newServedEnv(t, "hxdw")
	ctx := t.Context()
	deleter, err := env.subject.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}

	const blocker, dependent = "hxdw-blocker", "hxdw-dependent"
	seedServedIssue(t, ctx, env, blocker, types.StatusOpen)
	seedServedWisp(t, ctx, env, dependent)
	seedServedEdge(t, ctx, env, dependent, blocker)
	assertServedEdgeRows(t, ctx, env, "wisp_dependencies", 1, dependent, blocker)

	for _, dryRun := range []bool{false, true} {
		result, err := deleter.Delete(ctx, issueops.DeleteRequest{IDs: []string{blocker}, DryRun: dryRun})
		assertServedDependentsRefusal(t, err, dryRun)
		if result.Deleted != 0 {
			t.Errorf("refused delete (dryRun=%v) reported Deleted = %d, want 0", dryRun, result.Deleted)
		}
		assertServedIssueRows(t, ctx, env, 1, blocker)
		assertServedWispRows(t, ctx, env, 1, dependent)
		assertServedEdgeRows(t, ctx, env, "wisp_dependencies", 1, dependent, blocker)
	}

	forced, err := deleter.Delete(ctx, issueops.DeleteRequest{IDs: []string{blocker}, Force: true})
	if err != nil {
		t.Fatalf("forced delete of the durable row: %v", err)
	}
	if forced.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 — force deletes the NAMED durable row and nothing else", forced.Deleted)
	}
	if !reflect.DeepEqual(forced.Orphaned, []string{dependent}) {
		t.Errorf("Orphaned = %v, want [%s] — the orphan is a wisp, and it is still an orphan", forced.Orphaned, dependent)
	}
	assertServedIssueRows(t, ctx, env, 0, blocker)
	assertServedWispRows(t, ctx, env, 1, dependent)
	assertServedEdgeRows(t, ctx, env, "wisp_dependencies", 0, dependent, blocker)
}

// assertServedDependentsRefusal is the coarse shape L-delete-dependents leaves:
// ErrValidation, and NOT the typed sentinel the local role raises.
func assertServedDependentsRefusal(t *testing.T, err error, dryRun bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("an unforced delete (dryRun=%v) over a cross-plane dependent succeeded; the guard has no wisp exemption at either end", dryRun)
	}
	if !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("refusal (dryRun=%v) = %v, want ErrValidation — the wire spells this as a 400 invalid_argument", dryRun, err)
	}
	if errors.Is(err, issueops.ErrDependentsOutsideRequest) {
		t.Errorf("the refusal (dryRun=%v) carries ErrDependentsOutsideRequest; L-delete-dependents says the wire cannot express it — retire the row rather than the assertion", dryRun)
	}
}

// ── BatchCreator ────────────────────────────────────────────────────────────

func newServedBatchCreatorFixture(t *testing.T, prefix string) conformance.BatchCreatorFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	creator, err := env.subject.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	return conformance.BatchCreatorFixture{
		IssuePrefix:  env.prefix,
		BatchCreator: creator,
		CreateIssue:  env.createIssue,
		QueryScalar:  env.queryScalar,
		CountHistory: env.countHistory,
	}
}

// TestServedBatchCreateStampsEachItemsCreatedByFromTheActor is the served half
// of created_by on the batch: BatchCreateItem publishes no created_by, so the
// stored creator exists only because the server stamps it from the actor
// (internal/httpapi's batch_create.go). An item whose CreatedBy names that actor
// — the shape `bd create --file` sends — is carried by the stamp rather than
// refused, and an item that names none is stamped all the same.
func TestServedBatchCreateStampsEachItemsCreatedByFromTheActor(t *testing.T) {
	env := newServedEnv(t, "hbcby")
	ctx := t.Context()
	creator, err := env.subject.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}

	res, err := creator.CreateBatch(ctx, issueops.CreateBatchRequest{
		Actor: "file-writer",
		Items: []issueops.BatchCreateItem{
			{Issue: &issueops.Issue{Title: "named", Priority: 2, IssueType: types.TypeTask, CreatedBy: "file-writer"}},
			{Issue: &issueops.Issue{Title: "unnamed", Priority: 2, IssueType: types.TypeTask}},
		},
	})
	if err != nil {
		t.Fatalf("CreateBatch in bd create --file's shape = %v, want it served", err)
	}
	for i, issue := range res.Issues {
		stored, err := env.getIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("read back items[%d] %s: %v", i, issue.ID, err)
		}
		if stored.CreatedBy != "file-writer" {
			t.Errorf("items[%d] stored created_by = %q, want the actor %q", i, stored.CreatedBy, "file-writer")
		}
	}
}

func TestServedBatchCreatorCreatesEveryItemAsOneAct(t *testing.T) {
	conformance.RunBatchCreatorCreatesEveryItemAsOneAct(t, t.Context(), newServedBatchCreatorFixture(t, "hb00"))
}

func TestServedBatchCreatorRefusesEverythingWhenOneItemRefuses(t *testing.T) {
	skipKnownDivergence(t, "W-BatchCreateItem.Issue", bulkParkBead, parkBatchExplicitID)
	conformance.RunBatchCreatorRefusesEverythingWhenOneItemRefuses(t, t.Context(), newServedBatchCreatorFixture(t, "hb01"))
}

func TestServedBatchCreatorRejectsAnUnusableRequest(t *testing.T) {
	conformance.RunBatchCreatorRejectsAnUnusableRequest(t, t.Context(), newServedBatchCreatorFixture(t, "hb02"))
}

func TestServedBatchCreatorRefusesACrossPlaneInBatchEdge(t *testing.T) {
	skipKnownDivergence(t, "W-BatchCreateItem.Issue", bulkParkBead,
		"the case seeds an item with Issue.Ephemeral, and the wire's item publishes no wisp member at all — "+
			"so the plane an item lands in is the server's to choose and a cross-plane in-batch edge cannot be asked for. "+parkBatchExplicitID)
	conformance.RunBatchCreatorRefusesACrossPlaneInBatchEdge(t, t.Context(), newServedBatchCreatorFixture(t, "hb03"))
}

func TestServedBatchCreatorLinksAnEarlierItemOfTheSameBatch(t *testing.T) {
	skipKnownDivergence(t, "L-batchcreate-inbatch", bulkParkBead,
		"an edge onto an earlier item of the same batch needs that item to have named an id for itself, and BatchCreateItem publishes no id member (asserted by TestBatchCreateCannotNameAnItemOfItsOwnBatch)")
	conformance.RunBatchCreatorLinksAnEarlierItemOfTheSameBatch(t, t.Context(), newServedBatchCreatorFixture(t, "hb04"))
}

func TestServedBatchCreatorRefusesAnAbsentEdgeTarget(t *testing.T) {
	skipKnownDivergence(t, "L-batchcreate-target", bulkParkBead,
		"the case binds ErrValidation WRAPPING ErrNotFound, and the wire answers a dangling target with a plain 400 invalid_argument (asserted by TestServedBatchCreateRefusesAnAbsentEdgeTargetAsValidation). "+parkBatchExplicitID)
	conformance.RunBatchCreatorRefusesAnAbsentEdgeTarget(t, t.Context(), newServedBatchCreatorFixture(t, "hb05"))
}

func TestServedBatchCreatorAcceptsAForeignEdgeTarget(t *testing.T) {
	skipKnownDivergence(t, "W-BatchCreateItem.Issue", bulkParkBead,
		parkBatchExplicitID+" The rule itself is asserted by TestServedBatchCreateAcceptsAForeignEdgeTarget, over server-generated ids.")
	conformance.RunBatchCreatorAcceptsAForeignEdgeTarget(t, t.Context(), newServedBatchCreatorFixture(t, "hb06"))
}

func TestServedBatchCreatorRecordsOneHistoryEntry(t *testing.T) {
	skipKnownDivergence(t, "W-CreateBatchRequest.Provenance", bulkParkBead,
		"half the case sends CreateBatchRequest.Provenance, which batchCreateIssues publishes no member for, so the client refuses it rather than letting the server's own label stand in silence. "+
			"The one-entry-per-batch half is asserted by TestServedBatchCreateRecordsOneHistoryEntry.")
	conformance.RunBatchCreatorRecordsOneHistoryEntry(t, t.Context(), newServedBatchCreatorFixture(t, "hb07"))
}

func TestServedBatchCreatorRecordsNoHistoryForAnEphemeralBatch(t *testing.T) {
	skipKnownDivergence(t, "W-BatchCreateItem.Issue", bulkParkBead,
		"the case seeds Issue.Ephemeral on every item, and the wire's item publishes no wisp member. "+parkBatchExplicitID)
	conformance.RunBatchCreatorRecordsNoHistoryForAnEphemeralBatch(t, t.Context(), newServedBatchCreatorFixture(t, "hb08"))
}

// TestServedBatchCreatorEchoesSubSecondTimestamps is the batch half of the
// sub-second echo, parked on the item vocabulary like the eight above: its
// precision pin is a caller-supplied CreatedAt/UpdatedAt on every item, and
// apigen.BatchCreateItem carries no timestamp member. The client refuses
// items[0].Issue.CreatedAt before any request is sent, so there is no echo to
// measure — the case never reaches the wire, let alone the column.
func TestServedBatchCreatorEchoesSubSecondTimestamps(t *testing.T) {
	skipKnownDivergence(t, "W-BatchCreateItem.Issue", bulkParkBead,
		"the case seeds Issue.CreatedAt/UpdatedAt on every item, and the wire's item publishes no timestamp "+
			"member — the client refuses items[0].Issue.CreatedAt before any request is sent, so the echo it "+
			"asserts has nothing to echo. "+parkBatchExplicitID)
	conformance.RunBatchCreatorEchoesSubSecondTimestamps(t, t.Context(), newServedBatchCreatorFixture(t, "hb12"))
}

func TestServedBatchCreatorAppliesTheDefaultPriority(t *testing.T) {
	conformance.RunBatchCreatorAppliesTheDefaultPriority(t, t.Context(), newServedBatchCreatorFixture(t, "hbprio"))
}

// The last TWO items of the wire's item vocabulary, both on the EPHEMERAL half
// of it and both parked for the reason the eight above are: apigen.BatchCreateItem
// carries eight content members and no id and no wisp flag, so an item cannot
// ask for the plane it lands on and cannot be referred to by a later item.
//
// The ephemeral plane's own batch behavior is therefore unreachable from this
// role over http rather than merely unasserted, and no bespoke case stands in
// for it: there is no server-generated-id spelling of "put this item in the
// wisps table". That is what makes these the two parks with nothing beside them.

func TestServedBatchCreatorKeepsAnEphemeralItemsLabelsOffTheDurablePlane(t *testing.T) {
	skipKnownDivergence(t, "W-BatchCreateItem.Issue", bulkParkBead,
		"the case's single item sets Issue.Ephemeral, and the wire's item publishes no wisp member at all — "+
			"so there is no request that puts a batch-created row on the ephemeral plane, and the labels this "+
			"case follows there have nowhere to land. "+parkBatchExplicitID)
	conformance.RunBatchCreatorKeepsAnEphemeralItemsLabelsOffTheDurablePlane(t, t.Context(), newServedBatchCreatorFixture(t, "hb10"))
}

func TestServedBatchCreatorLinksAnEarlierItemOnTheEphemeralPlane(t *testing.T) {
	skipKnownDivergence(t, "L-batchcreate-inbatch", bulkParkBead,
		"it is the in-batch edge of TestServedBatchCreatorLinksAnEarlierItemOfTheSameBatch on the ephemeral "+
			"plane, so it needs BOTH members the item vocabulary withholds: an id for the earlier item to be "+
			"named by, and a wisp flag for either item to land on that plane at all")
	conformance.RunBatchCreatorLinksAnEarlierItemOnTheEphemeralPlane(t, t.Context(), newServedBatchCreatorFixture(t, "hb11"))
}

func TestServedBatchCreatorDoesNotMutateTheCallerRequest(t *testing.T) {
	skipKnownDivergence(t, "W-CreateBatchRequest.Provenance", bulkParkBead,
		"the case sends CreateBatchRequest.Provenance, which the client refuses; the no-mutation promise it is really about — the assigned id lands on the RESULT and never on the caller's issue — is asserted by TestBatchCreateDoesNotWriteThroughTheCallersItems.")
	conformance.RunBatchCreatorDoesNotMutateTheCallerRequest(t, t.Context(), newServedBatchCreatorFixture(t, "hb09"))
}

const parkBatchExplicitID = "the case names its items' ids and passes ForceIDPrefix (the contract's own preamble says every case does), and neither is expressible: BatchCreateItem publishes no id member, so ids over http are the server's to assign."

// TestServedBatchCreateAcceptsAForeignEdgeTarget is the foreign-target half of
// the edge clause, over SERVER-GENERATED ids — the shape an http workspace can
// actually ask for.
//
// Both spellings are asserted because they are one rule: a target this database
// was never going to hold is not a miss, whether it is an `external:` reference
// or an id belonging to another repository.
func TestServedBatchCreateAcceptsAForeignEdgeTarget(t *testing.T) {
	env := newServedEnv(t, "hbext")
	ctx := t.Context()
	creator, err := env.subject.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}

	const externalTarget, foreignTarget = "external:JIRA-4471", "otherrig-9910"
	result, err := creator.CreateBatch(ctx, issueops.CreateBatchRequest{
		Actor: "batch-writer",
		Items: []issueops.BatchCreateItem{
			{
				Issue:        &issueops.Issue{Title: "depends on something outside beads", Priority: 2, IssueType: types.TypeTask},
				Dependencies: []issueops.CreateDependency{{TargetID: externalTarget, Type: types.DepBlocks}},
			},
			{
				Issue:        &issueops.Issue{Title: "depends on another rig", Priority: 2, IssueType: types.TypeTask},
				Dependencies: []issueops.CreateDependency{{TargetID: foreignTarget, Type: types.DepBlocks}},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateBatch with foreign edge targets: %v", err)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("CreateBatch returned %d issues, want 2", len(result.Issues))
	}
	// The target column depends on what the target IS, and neither of these is a
	// stored row: an `external:` reference and a foreign-prefix id both land in
	// depends_on_external, which is the whole point of the rule.
	for i, target := range []string{externalTarget, foreignTarget} {
		var edges int
		if err := env.queryScalar(ctx,
			`SELECT COUNT(*) FROM dependencies WHERE issue_id = ?
				AND (depends_on_issue_id = ? OR depends_on_external = ?)`,
			[]any{result.Issues[i].ID, target, target}, &edges); err != nil {
			t.Fatalf("count the edge onto %s: %v", target, err)
		}
		if edges != 1 {
			t.Errorf("edges onto %s = %d, want 1", target, edges)
		}
	}
}

// TestServedBatchCreateRefusesAnAbsentEdgeTargetAsValidation is
// L-batchcreate-target's pin, and it RUNS.
//
// The half that still holds is the half worth having: a target naming nothing
// takes the WHOLE batch down, so a client cannot end up with issues whose
// declared relationships are missing. Only the second sentinel is lost.
func TestServedBatchCreateRefusesAnAbsentEdgeTargetAsValidation(t *testing.T) {
	env := newServedEnv(t, "hbmiss")
	ctx := t.Context()
	creator, err := env.subject.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}

	_, err = creator.CreateBatch(ctx, issueops.CreateBatchRequest{
		Actor: "batch-writer",
		Items: []issueops.BatchCreateItem{
			{Issue: &issueops.Issue{Title: "would land", Priority: 2, IssueType: types.TypeTask}},
			{
				Issue:        &issueops.Issue{Title: "names something absent", Priority: 2, IssueType: types.TypeTask},
				Dependencies: []issueops.CreateDependency{{TargetID: "hbmiss-absent", Type: types.DepBlocks}},
			},
		},
	})
	if err == nil {
		t.Fatal("a batch whose edge target names nothing was accepted; a create that dropped an edge is data loss the caller cannot learn about")
	}
	if !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("refusal = %v, want ErrValidation", err)
	}
	if errors.Is(err, issueops.ErrNotFound) {
		t.Error("the refusal wraps ErrNotFound; L-batchcreate-target says the wire cannot express it — retire the row rather than the assertion")
	}

	// The sibling item is what an implementation that dropped the edge and kept
	// the issues would leave behind.
	var created int
	if err := env.queryScalar(ctx, "SELECT COUNT(*) FROM issues", nil, &created); err != nil {
		t.Fatalf("count the issues: %v", err)
	}
	if created != 0 {
		t.Errorf("the refused batch created %d issue(s); it is all or nothing", created)
	}
}

// TestServedBatchCreateRecordsOneHistoryEntry is the durable half of "at most
// one history entry", over a request with no Provenance — which is the only
// shape http can send.
func TestServedBatchCreateRecordsOneHistoryEntry(t *testing.T) {
	env := newServedEnv(t, "hbhist")
	ctx := t.Context()
	creator, err := env.subject.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}

	before, err := env.countHistory(ctx)
	if err != nil {
		t.Fatalf("count history: %v", err)
	}
	if _, err := creator.CreateBatch(ctx, issueops.CreateBatchRequest{
		Actor: "batch-writer",
		Items: []issueops.BatchCreateItem{
			{Issue: &issueops.Issue{Title: "one", Priority: 2, IssueType: types.TypeTask}},
			{Issue: &issueops.Issue{Title: "two", Priority: 2, IssueType: types.TypeTask}},
			{Issue: &issueops.Issue{Title: "three", Priority: 2, IssueType: types.TypeTask}},
		},
	}); err != nil {
		t.Fatalf("CreateBatch(3 durable items): %v", err)
	}
	after, err := env.countHistory(ctx)
	if err != nil {
		t.Fatalf("count history: %v", err)
	}
	if delta := after - before; delta != 1 {
		t.Errorf("history entries += %d for a 3-item batch, want 1: the request is the transaction, so it records one entry", delta)
	}
}

// The raw-row hooks the delete cases need. They read through the REFERENCE
// store's own SQL handle, because whether a row is really gone is the one claim
// a delete result cannot be trusted to make about itself.

func seedServedWisp(t *testing.T, ctx context.Context, env *servedEnv, id string) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := env.createWisp(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
}

// seedServedEdge makes dependent depend on blocker. The PLANE is the source's,
// resolved by the reference store itself, which is why a case can seed a
// cross-plane edge without naming a table.
func seedServedEdge(t *testing.T, ctx context.Context, env *servedEnv, dependent, blocker string) {
	t.Helper()
	if err := env.addDependency(ctx, &types.Dependency{
		IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks,
	}, "seed"); err != nil {
		t.Fatalf("seed the edge %s -> %s: %v", dependent, blocker, err)
	}
}

func assertServedIssueRows(t *testing.T, ctx context.Context, env *servedEnv, want int, ids ...string) {
	t.Helper()
	assertServedPlaneRows(t, ctx, env, "issues", want, ids...)
}

func assertServedWispRows(t *testing.T, ctx context.Context, env *servedEnv, want int, ids ...string) {
	t.Helper()
	assertServedPlaneRows(t, ctx, env, "wisps", want, ids...)
}

//nolint:gosec // G201: table is chosen by the caller from the two plane tables.
func assertServedPlaneRows(t *testing.T, ctx context.Context, env *servedEnv, table string, want int, ids ...string) {
	t.Helper()
	for _, id := range ids {
		var rows int
		query := "SELECT COUNT(*) FROM " + table + " WHERE id = ?"
		if err := env.queryScalar(ctx, query, []any{id}, &rows); err != nil {
			t.Fatalf("count %s in %s: %v", id, table, err)
		}
		if rows != want {
			t.Errorf("%s rows for %s = %d, want %d", table, id, rows, want)
		}
	}
}

// assertServedEdgeRows counts one edge in the plane table its SOURCE lives in.
// The target column is COALESCEd across the three the schema publishes, so a
// cross-plane edge is counted whichever end of it is a wisp.
//
//nolint:gosec // G201: table is chosen by the caller from the two edge tables.
func assertServedEdgeRows(t *testing.T, ctx context.Context, env *servedEnv, table string, want int, dependent, blocker string) {
	t.Helper()
	var rows int
	query := "SELECT COUNT(*) FROM " + table + " WHERE issue_id = ? AND " +
		"COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?"
	if err := env.queryScalar(ctx, query, []any{dependent, blocker}, &rows); err != nil {
		t.Fatalf("count %s edges %s -> %s: %v", table, dependent, blocker, err)
	}
	if rows != want {
		t.Errorf("%s rows %s -> %s = %d, want %d", table, dependent, blocker, rows, want)
	}
}
