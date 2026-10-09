//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_memories_deps_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Memories and DependencyEditor contracts against the served surface.
//
// Both roles map onto the wire whole, so almost every case runs for real; the
// handful that do not are named individually with the member the wire does not
// publish. The dependency block is the largest contract in the suite and most
// of it is about SERVER-side graph semantics — plane routing, the cycle gates,
// history entries — which is exactly what running it through the client proves
// is not disturbed by the crossing.

func newServedMemoriesFixture(t *testing.T, prefix string) conformance.MemoriesFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	memories, err := env.subject.Memories()
	if err != nil {
		t.Fatalf("Memories(): %v", err)
	}
	return conformance.MemoriesFixture{
		IssuePrefix:  env.prefix,
		Memories:     memories,
		SetConfig:    env.setConfig,
		QueryScalar:  env.queryScalar,
		CountHistory: env.countHistory,
	}
}

func TestServedMemoriesListOfAnEmptyPlaneAnswersAnEmptyMap(t *testing.T) {
	conformance.RunMemoriesListOfAnEmptyPlaneAnswersAnEmptyMap(t, t.Context(), newServedMemoriesFixture(t, "hm00"))
}

func TestServedMemoriesRememberStoresContentVerbatim(t *testing.T) {
	conformance.RunMemoriesRememberStoresContentVerbatim(t, t.Context(), newServedMemoriesFixture(t, "hm01"))
}

func TestServedMemoriesRememberDerivesTheKeyWhenAbsent(t *testing.T) {
	conformance.RunMemoriesRememberDerivesTheKeyWhenAbsent(t, t.Context(), newServedMemoriesFixture(t, "hm02"))
}

func TestServedMemoriesRememberWithExplicitKeyStoresVerbatim(t *testing.T) {
	conformance.RunMemoriesRememberWithExplicitKeyStoresVerbatim(t, t.Context(), newServedMemoriesFixture(t, "hm03"))
}

func TestServedMemoriesRememberReplacesAndReportsIt(t *testing.T) {
	conformance.RunMemoriesRememberReplacesAndReportsIt(t, t.Context(), newServedMemoriesFixture(t, "hm04"))
}

func TestServedMemoriesRememberRefusesEmptyContent(t *testing.T) {
	conformance.RunMemoriesRememberRefusesEmptyContent(t, t.Context(), newServedMemoriesFixture(t, "hm05"))
}

func TestServedMemoriesRememberRefusesAWhitespaceOnlyKey(t *testing.T) {
	conformance.RunMemoriesRememberRefusesAWhitespaceOnlyKey(t, t.Context(), newServedMemoriesFixture(t, "hm06"))
}

func TestServedMemoriesRememberRefusesAnUnderivableKey(t *testing.T) {
	conformance.RunMemoriesRememberRefusesAnUnderivableKey(t, t.Context(), newServedMemoriesFixture(t, "hm07"))
}

func TestServedMemoriesRecallAnswersTheStoredValue(t *testing.T) {
	conformance.RunMemoriesRecallAnswersTheStoredValue(t, t.Context(), newServedMemoriesFixture(t, "hm08"))
}

func TestServedMemoriesRecallReportsAMissAsNotFoundNotAnError(t *testing.T) {
	conformance.RunMemoriesRecallReportsAMissAsNotFoundNotAnError(t, t.Context(), newServedMemoriesFixture(t, "hm09"))
}

func TestServedMemoriesRecallConflatesStoredEmptyWithAbsent(t *testing.T) {
	conformance.RunMemoriesRecallConflatesStoredEmptyWithAbsent(t, t.Context(), newServedMemoriesFixture(t, "hm10"))
}

func TestServedMemoriesForgetRemovesExactlyTheNamedRow(t *testing.T) {
	conformance.RunMemoriesForgetRemovesExactlyTheNamedRow(t, t.Context(), newServedMemoriesFixture(t, "hm11"))
}

func TestServedMemoriesForgetNeverTouchesTheSettingsPlane(t *testing.T) {
	conformance.RunMemoriesForgetNeverTouchesTheSettingsPlane(t, t.Context(), newServedMemoriesFixture(t, "hm12"))
}

func TestServedMemoriesForgetReportsTheForgottenValue(t *testing.T) {
	conformance.RunMemoriesForgetReportsTheForgottenValue(t, t.Context(), newServedMemoriesFixture(t, "hm13"))
}

func TestServedMemoriesForgetOfAnAbsentKeyIsNotFoundAndDeletesNothing(t *testing.T) {
	conformance.RunMemoriesForgetOfAnAbsentKeyIsNotFoundAndDeletesNothing(t, t.Context(), newServedMemoriesFixture(t, "hm14"))
}

func TestServedMemoriesListReturnsOnlyTheMemoryPlane(t *testing.T) {
	conformance.RunMemoriesListReturnsOnlyTheMemoryPlane(t, t.Context(), newServedMemoriesFixture(t, "hm15"))
}

func TestServedMemoriesListSearchMatchesTheUserKeyNotTheStorageKey(t *testing.T) {
	conformance.RunMemoriesListSearchMatchesTheUserKeyNotTheStorageKey(t, t.Context(), newServedMemoriesFixture(t, "hm16"))
}

func TestServedMemoriesListSearchMatchesKeyOrValueCaseInsensitively(t *testing.T) {
	conformance.RunMemoriesListSearchMatchesKeyOrValueCaseInsensitively(t, t.Context(), newServedMemoriesFixture(t, "hm17"))
}

func TestServedMemoriesARefusedWriteRecordsNoHistory(t *testing.T) {
	conformance.RunMemoriesARefusedWriteRecordsNoHistory(t, t.Context(), newServedMemoriesFixture(t, "hm18"))
}

func newServedDependencyEditorFixture(t *testing.T, prefix string) conformance.DependencyEditorFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	editor, err := env.subject.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	return conformance.DependencyEditorFixture{
		IssuePrefix: env.prefix,
		Editor:      editor,
		CreateIssue: env.createIssue,
		CreateWisp:  env.createWisp,
		// The one precondition the ROLE's own request type cannot express: a
		// DependencyEdge carries no metadata, so a waits-for edge with an
		// any-children gate on it can only be seeded out of band. The verb under
		// test in the case that needs it is still AddDependencies.
		AddDependency: env.addDependency,
		QueryScalar:   env.queryScalar,
		CountHistory:  env.countHistory,
		// The idempotency cases turn journaling on to prove a no-op journals
		// nothing. Journaling is the SERVER's (the reference store's) switch;
		// the harness serves the journal-enabled flag already.
		SetJournalEnabled: func(enabled bool) { env.reference.SetEventsJournalEnabled(enabled) },
		// The same-type re-add cases (ChangedMetadataMintsOneVersion,
		// IdenticalMetadataIsANoOp) drive their re-add through
		// fixture.AddDependency — the reference store's own raw path, same as
		// AddDependency above — rather than through the role, because
		// publicops.DependencyEdge carries no metadata field. That means dual-write
		// version history is the reference store's own switch, same as journaling,
		// so it wires the same way.
		SetVersionedHistoryEnabled: env.reference.SetVersionedHistoryEnabled,
	}
}

func TestServedDependencyEditorRoutesWispSourcedEdgeToTheWispPlane(t *testing.T) {
	conformance.RunDependencyEditorRoutesWispSourcedEdgeToTheWispPlane(t, t.Context(), newServedDependencyEditorFixture(t, "hd00"))
}

func TestServedDependencyEditorMixedBatchWritesBothPlanes(t *testing.T) {
	conformance.RunDependencyEditorMixedBatchWritesBothPlanes(t, t.Context(), newServedDependencyEditorFixture(t, "hd01"))
}

func TestServedDependencyEditorMixedBatchRefusalRollsBackBothPlanes(t *testing.T) {
	conformance.RunDependencyEditorMixedBatchRefusalRollsBackBothPlanes(t, t.Context(), newServedDependencyEditorFixture(t, "hd02"))
}

func TestServedDependencyEditorRefusesCrossPlaneCycle(t *testing.T) {
	skipKnownDivergence(t, "W-AddDependenciesRequest.SkipPerEdgeCycleCheck", parkBead,
		parkSkipPerEdgeProbe)
	conformance.RunDependencyEditorRefusesCrossPlaneCycle(t, t.Context(), newServedDependencyEditorFixture(t, "hd03"))
}

func TestServedDependencyEditorAddedEchoesTheRequestOrder(t *testing.T) {
	conformance.RunDependencyEditorAddedEchoesTheRequestOrder(t, t.Context(), newServedDependencyEditorFixture(t, "hd04"))
}

func TestServedDependencyEditorSameTypeReAddIsIdempotent(t *testing.T) {
	conformance.RunDependencyEditorSameTypeReAddIsIdempotent(t, t.Context(), newServedDependencyEditorFixture(t, "hd05"))
}

func TestServedDependencyEditorRepeatsWithinOneRequestCollapse(t *testing.T) {
	conformance.RunDependencyEditorRepeatsWithinOneRequestCollapse(t, t.Context(), newServedDependencyEditorFixture(t, "hd06"))
}

func TestServedDependencyEditorAttributesItsEventsToTheActor(t *testing.T) {
	conformance.RunDependencyEditorAttributesItsEventsToTheActor(t, t.Context(), newServedDependencyEditorFixture(t, "hd07"))
}

func TestServedDependencyEditorRetypeRefusalLeavesTheOriginalEdge(t *testing.T) {
	conformance.RunDependencyEditorRetypeRefusalLeavesTheOriginalEdge(t, t.Context(), newServedDependencyEditorFixture(t, "hd08"))
}

func TestServedDependencyEditorRefusalWritesNothing(t *testing.T) {
	skipKnownDivergence(t, "W-AddDependenciesRequest.SkipPerEdgeCycleCheck", parkBead,
		parkSkipPerEdgeProbe)
	conformance.RunDependencyEditorRefusalWritesNothing(t, t.Context(), newServedDependencyEditorFixture(t, "hd09"))
}

func TestServedDependencyEditorRemoveIsIdempotent(t *testing.T) {
	conformance.RunDependencyEditorRemoveIsIdempotent(t, t.Context(), newServedDependencyEditorFixture(t, "hd10"))
}

func TestServedDependencyEditorAppliesParentChildBeforeBlockingEdges(t *testing.T) {
	conformance.RunDependencyEditorAppliesParentChildBeforeBlockingEdges(t, t.Context(), newServedDependencyEditorFixture(t, "hd11"))
}

func TestServedDependencyEditorAcceptsAnExternalTarget(t *testing.T) {
	conformance.RunDependencyEditorAcceptsAnExternalTarget(t, t.Context(), newServedDependencyEditorFixture(t, "hd12"))
}

func TestServedDependencyEditorAcceptsAForeignRepoTarget(t *testing.T) {
	conformance.RunDependencyEditorAcceptsAForeignRepoTarget(t, t.Context(), newServedDependencyEditorFixture(t, "hd13"))
}

func TestServedDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy(t *testing.T) {
	conformance.RunDependencyEditorRefusesBlockingEdgeAcrossItsOwnHierarchy(t, t.Context(), newServedDependencyEditorFixture(t, "hd14"))
}

func TestServedDependencyEditorRefusesSelfDependencyWithTheProbeSkipped(t *testing.T) {
	skipKnownDivergence(t, "W-AddDependenciesRequest.SkipPerEdgeCycleCheck", parkBead,
		parkSkipPerEdgeProbe)
	conformance.RunDependencyEditorRefusesSelfDependencyWithTheProbeSkipped(t, t.Context(), newServedDependencyEditorFixture(t, "hd15"))
}

func TestServedDependencyEditorRecordsOneHistoryEntryPerLandedRequest(t *testing.T) {
	conformance.RunDependencyEditorRecordsOneHistoryEntryPerLandedRequest(t, t.Context(), newServedDependencyEditorFixture(t, "hd16"))
}

func TestServedDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest(t *testing.T) {
	conformance.RunDependencyEditorRecordsNoHistoryForAnAllEphemeralRequest(t, t.Context(), newServedDependencyEditorFixture(t, "hd17"))
}

func TestServedDependencyEditorSnapshotsTheRequest(t *testing.T) {
	conformance.RunDependencyEditorSnapshotsTheRequest(t, t.Context(), newServedDependencyEditorFixture(t, "hd18"))
}

func TestServedDependencyEditorValidationRefusalsWriteNothing(t *testing.T) {
	conformance.RunDependencyEditorValidationRefusalsWriteNothing(t, t.Context(), newServedDependencyEditorFixture(t, "hd19"))
}

func TestServedDependencyEditorRoutesWispSourcedRemovalToTheWispPlane(t *testing.T) {
	conformance.RunDependencyEditorRoutesWispSourcedRemovalToTheWispPlane(t, t.Context(), newServedDependencyEditorFixture(t, "hd20"))
}

// The two endpoint-not-found cases park on their SENTINEL only. The behavior
// they are really about — the request fails and writes nothing — is asserted
// for real by TestServedDependencyEndpointNotFoundRefusesAsValidation below, so
// ledger row L-dep-endpoint retires loudly rather than sitting behind a skip.

func TestServedDependencyEditorRefusesAGhostSource(t *testing.T) {
	skipKnownDivergence(t, "L-dep-endpoint", parkBead, parkEndpointSentinel)
	conformance.RunDependencyEditorRefusesAGhostSource(t, t.Context(), newServedDependencyEditorFixture(t, "hd21"))
}

func TestServedDependencyEditorRefusesAMissingLocalTarget(t *testing.T) {
	skipKnownDivergence(t, "L-dep-endpoint", parkBead, parkEndpointSentinel)
	conformance.RunDependencyEditorRefusesAMissingLocalTarget(t, t.Context(), newServedDependencyEditorFixture(t, "hd22"))
}

// parkSkipPerEdgeProbe is the one reason FOUR cases here share, written once
// because it is one decision: the wire leaves SkipPerEdgeCycleCheck unpublished
// on purpose — an unauthenticated surface is where a default has to be the
// guarded one — so the client refuses the member rather than dropping it, and
// every case that binds the flag is unreachable whole.
const parkSkipPerEdgeProbe = "the case binds AddDependenciesRequest.SkipPerEdgeCycleCheck, which the wire leaves UNPUBLISHED by design — an unauthenticated surface is where a default must be the guarded one — so the client refuses the member rather than dropping it and the whole case is unreachable"

const parkEndpointSentinel = "the wire flattens the endpoint-not-found refusal into a 400 invalid_argument with reason invalid_value, so ErrDependencySourceNotFound and ErrDependencyTargetNotFound cannot be reconstructed without parsing detail prose (asserted by TestServedDependencyEndpointNotFoundRefusesAsValidation)"

// TestServedDependencyEndpointNotFoundRefusesAsValidation is L-dep-endpoint's
// pin, and it RUNS.
//
// The row is a divergence in CLASSIFICATION, not in outcome, and both parts need
// saying. The part that still holds is the part that matters most: an edge whose
// source or target names nothing fails, and the graph is untouched — a client
// that quietly wrote a half-graph would be a far worse divergence than a coarse
// sentinel. The part that diverges is the sentinel itself, and asserting the
// absence is what makes the row retire the day the wire grows a distinguishing
// code or reason.
func TestServedDependencyEndpointNotFoundRefusesAsValidation(t *testing.T) {
	env := newServedEnv(t, "hdep")
	ctx := t.Context()
	editor, err := env.subject.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}

	const real = "hdep-real"
	seedServedIssue(t, ctx, env, real, types.StatusOpen)

	cases := map[string]struct {
		edge     issueops.DependencyEdge
		sentinel error
	}{
		"ghost source": {
			edge:     issueops.DependencyEdge{IssueID: "hdep-ghost", DependsOnID: real, Type: issueops.DepBlocks},
			sentinel: issueops.ErrDependencySourceNotFound,
		},
		"missing local target": {
			edge:     issueops.DependencyEdge{IssueID: real, DependsOnID: "hdep-absent", Type: issueops.DepBlocks},
			sentinel: issueops.ErrDependencyTargetNotFound,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{
				Actor: "writer", Edges: []issueops.DependencyEdge{tc.edge},
			})
			if err == nil {
				t.Fatal("the add succeeded; an endpoint that names nothing must refuse")
			}
			if !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("refusal = %v, want ErrValidation — the wire spells this as a 400 invalid_argument", err)
			}
			if errors.Is(err, tc.sentinel) {
				t.Errorf("refusal carries %v; L-dep-endpoint says the wire cannot express it — retire the row rather than the assertion", tc.sentinel)
			}

			// Both endpoints, because either half of a refused edge landing is
			// the same bug. The target column is the DURABLE one: the wisp
			// plane has its own (depends_on_wisp_id) and neither id here names
			// a wisp.
			var edges int
			if err := env.queryScalar(ctx,
				"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? OR depends_on_issue_id = ?",
				[]any{tc.edge.IssueID, tc.edge.DependsOnID}, &edges); err != nil {
				t.Fatalf("count the edges the refusal may have written: %v", err)
			}
			if edges != 0 {
				t.Errorf("the refused add left %d edge(s) behind; the refusal is coarse, not partial", edges)
			}
		})
	}
}

func TestServedDependencyEditorAcceptsATypeOutsideTheConstants(t *testing.T) {
	conformance.RunDependencyEditorAcceptsATypeOutsideTheConstants(t, t.Context(), newServedDependencyEditorFixture(t, "hd23"))
}

func TestServedDependencyEditorRemovesOnlyTheNamedEdge(t *testing.T) {
	conformance.RunDependencyEditorRemovesOnlyTheNamedEdge(t, t.Context(), newServedDependencyEditorFixture(t, "hd24"))
}

func TestServedDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe(t *testing.T) {
	skipKnownDivergence(t, "W-AddDependenciesRequest.SkipPerEdgeCycleCheck", parkBead,
		parkSkipPerEdgeProbe)
	conformance.RunDependencyEditorSkipPerEdgeCycleCheckDropsOnlyTheProbe(t, t.Context(), newServedDependencyEditorFixture(t, "hd25"))
}

func TestServedDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest(t *testing.T) {
	conformance.RunDependencyEditorRecordsOneHistoryEntryForAMixedPlaneRequest(t, t.Context(), newServedDependencyEditorFixture(t, "hd26"))
}

func TestServedDependencyEditorWritesTheTargetIntoItsTypedColumn(t *testing.T) {
	conformance.RunDependencyEditorWritesTheTargetIntoItsTypedColumn(t, t.Context(), newServedDependencyEditorFixture(t, "hd27"))
}

func TestServedDependencyEditorRefusesBlockingEdgeAcrossAWispHierarchy(t *testing.T) {
	conformance.RunDependencyEditorRefusesBlockingEdgeAcrossAWispHierarchy(t, t.Context(), newServedDependencyEditorFixture(t, "hd28"))
}

func TestServedDependencyEditorRefusesACycleThroughAParentChildHop(t *testing.T) {
	conformance.RunDependencyEditorRefusesACycleThroughAParentChildHop(t, t.Context(), newServedDependencyEditorFixture(t, "hd29"))
}

func TestServedDependencyEditorRefusesASamePlaneEdgeClosingACrossPlaneCycle(t *testing.T) {
	conformance.RunDependencyEditorRefusesASamePlaneEdgeClosingACrossPlaneCycle(t, t.Context(), newServedDependencyEditorFixture(t, "hd30"))
}

// The GRAPH-SHAPE and BLOCKED-STATE halves of this role, which nothing in the
// wiring above reached.
//
// Neither half is about a member: `dependencies:add` and `dependencies:remove`
// map their whole request, so what is left to prove is what the SERVER's
// transaction does with the edges it accepted. The four shape cases pin the
// edges an add must NOT refuse — a diamond is not a cycle, a blocking edge
// between two issue types is legal, and gate scope follows the edge type — and
// the four blocked-state cases pin the `is_blocked` projection the same add and
// remove maintain, read raw because no role hydrates that column.
//
// They are worth running here rather than only on the store legs for a reason
// this leg alone has: the client sends ONE request per case and recomputes
// nothing, so the whole settlement is the server's own closing transaction. A
// port that split a batch, reordered it, or dropped the descendant expansion
// would show up as a stale flag on a row nobody named.

func TestServedDependencyEditorAcceptsADiamond(t *testing.T) {
	conformance.RunDependencyEditorAcceptsADiamond(t, t.Context(), newServedDependencyEditorFixture(t, "hd31"))
}

func TestServedDependencyEditorAcceptsBlockingAcrossIssueTypes(t *testing.T) {
	conformance.RunDependencyEditorAcceptsBlockingAcrossIssueTypes(t, t.Context(), newServedDependencyEditorFixture(t, "hd32"))
}

func TestServedDependencyEditorRefusesADottedChildGatedOnItsOwnParent(t *testing.T) {
	conformance.RunDependencyEditorRefusesADottedChildGatedOnItsOwnParent(t, t.Context(), newServedDependencyEditorFixture(t, "hddot"))
}

func TestServedDependencyEditorGateScopeFollowsTheEdgeType(t *testing.T) {
	conformance.RunDependencyEditorGateScopeFollowsTheEdgeType(t, t.Context(), newServedDependencyEditorFixture(t, "hd33"))
}

func TestServedDependencyEditorAddMarksItsSourceInTheSameVerb(t *testing.T) {
	conformance.RunDependencyEditorAddMarksItsSourceInTheSameVerb(t, t.Context(), newServedDependencyEditorFixture(t, "hd34"))
}

func TestServedDependencyEditorRelatesToAddLeavesItsSourceUnblocked(t *testing.T) {
	conformance.RunDependencyEditorRelatesToAddLeavesItsSourceUnblocked(t, t.Context(), newServedDependencyEditorFixture(t, "hd35"))
}

func TestServedDependencyEditorRemoveUnmarksItsSourceAndDescendants(t *testing.T) {
	conformance.RunDependencyEditorRemoveUnmarksItsSourceAndDescendants(t, t.Context(), newServedDependencyEditorFixture(t, "hd36"))
}

func TestServedDependencyEditorMaintainsBlockedStateAcrossPlanes(t *testing.T) {
	conformance.RunDependencyEditorMaintainsBlockedStateAcrossPlanes(t, t.Context(), newServedDependencyEditorFixture(t, "hd37"))
}

// TestServedDependencyEditorClosedChildAddSatisfiesAnAnyChildrenGate is the one
// that needs the out-of-band edge seed: the gate it satisfies is metadata on a
// waits-for edge, and the role's own DependencyEdge carries none.
func TestServedDependencyEditorClosedChildAddSatisfiesAnAnyChildrenGate(t *testing.T) {
	conformance.RunDependencyEditorClosedChildAddSatisfiesAnAnyChildrenGate(t, t.Context(), newServedDependencyEditorFixture(t, "hd38"))
}

// The same-type re-add cases, newly wired: each drives the re-add through
// fixture.AddDependency (the reference store's own raw path, same reason
// ClosedChildAddSatisfiesAnAnyChildrenGate reaches past the role above) and
// need dual-write version history on, which the fixture now wires onto the
// same reference store SetJournalEnabled already uses.

func TestServedDependencyEditorSameTypeReAddWithChangedMetadataMintsOneVersion(t *testing.T) {
	conformance.RunDependencyEditorSameTypeReAddWithChangedMetadataMintsOneVersion(t, t.Context(), newServedDependencyEditorFixture(t, "hd39"))
}

func TestServedDependencyEditorSameTypeReAddWithIdenticalMetadataIsANoOp(t *testing.T) {
	conformance.RunDependencyEditorSameTypeReAddWithIdenticalMetadataIsANoOp(t, t.Context(), newServedDependencyEditorFixture(t, "hd40"))
}

func TestServedDependencyEditorSameTypeReAddWithChangedThreadMintsOneVersion(t *testing.T) {
	conformance.RunDependencyEditorSameTypeReAddWithChangedThreadMintsOneVersion(t, t.Context(), newServedDependencyEditorFixture(t, "hd41"))
}

// TestServedServerRefusesADottedChildGatedOnItsOwnParent sends the edge on the
// raw wire, past the client's own CheckDottedChildDependency, so what refuses
// it is the SERVER's role — the leg a non-bd HTTP caller (gc, curl) reaches.
// Before the rule moved into issueops only the CLI refused it, and this edge
// landed.
func TestServedServerRefusesADottedChildGatedOnItsOwnParent(t *testing.T) {
	env := newServedEnv(t, "hddotw")
	parent := env.prefix + "-dotted"
	child := parent + ".1"
	for _, id := range []string{parent, child} {
		if err := env.createIssue(t.Context(), &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	w, err := env.subject.roleWire("DependencyEditor")
	if err != nil {
		t.Fatalf("roleWire: %v", err)
	}
	_, err = w.AddDependencies(t.Context(), apigen.AddDependenciesRequest{
		Actor: "writer",
		Edges: []apigen.DependencyEdge{{IssueId: child, DependsOnId: parent, Type: "blocks"}},
	})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("raw-wire child -> parent blocks: error = %v, want the server's ErrValidation refusal", err)
	}
	var n int
	if err := env.queryScalar(t.Context(), "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", []any{child}, &n); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d edge(s) from %s landed; the server must refuse a dotted child gated on its own parent", n, child)
	}
}
