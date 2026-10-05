//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestCounterContract runs the Counter contract against the embedded store,
// which hands back the SAME body the server-backed store does
// (internal/workapi/storecounter) and differs only in the engine underneath.
// That is what this wiring catches; it is not an independent vote on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids are prefix-namespaced and every request
// is scoped to them, and the history delta needs the subtests sequential
// anyway.
func TestCounterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cnt")
	ctx := t.Context()
	fixture := newEmbeddedCounterFixture(t, te, "cnt")

	t.Run("CountsTheDurablePlaneByDefault", func(t *testing.T) {
		conformance.RunCounterCountsTheDurablePlaneByDefault(t, ctx, fixture)
	})
	t.Run("IncludeInfraMergesTheWispTier", func(t *testing.T) {
		conformance.RunCounterIncludeInfraMergesTheWispTier(t, ctx, fixture)
	})
	t.Run("IncludeInfraExcludesGates", func(t *testing.T) {
		conformance.RunCounterIncludeInfraExcludesGates(t, ctx, fixture)
	})
	t.Run("CountsClosedRows", func(t *testing.T) {
		conformance.RunCounterCountsClosedRows(t, ctx, fixture)
	})
	t.Run("AnUnknownStatusMatchesNothing", func(t *testing.T) {
		conformance.RunCounterAnUnknownStatusMatchesNothing(t, ctx, fixture)
	})
	t.Run("GroupsPartitionTheScalarSet", func(t *testing.T) {
		conformance.RunCounterGroupsPartitionTheScalarSet(t, ctx, fixture)
	})
	t.Run("LabelBucketsOverlapSoTotalIsNotTheirSum", func(t *testing.T) {
		conformance.RunCounterLabelBucketsOverlapSoTotalIsNotTheirSum(t, ctx, fixture)
	})
	t.Run("NamesTheEmptyBuckets", func(t *testing.T) {
		conformance.RunCounterNamesTheEmptyBuckets(t, ctx, fixture)
	})
	t.Run("PrefixesPriorityBuckets", func(t *testing.T) {
		conformance.RunCounterPrefixesPriorityBuckets(t, ctx, fixture)
	})
	t.Run("PriorityBucketsCountZeroAndCountEveryRow", func(t *testing.T) {
		conformance.RunCounterPriorityBucketsCountZeroAndCountEveryRow(t, ctx, fixture)
	})
	t.Run("TheNoLabelBucketIsAbsentWhenEveryRowIsLabeled", func(t *testing.T) {
		conformance.RunCounterTheNoLabelBucketIsAbsentWhenEveryRowIsLabeled(t, ctx, fixture)
	})
	t.Run("TypeBucketsAreTheRawTypeNames", func(t *testing.T) {
		conformance.RunCounterTypeBucketsAreTheRawTypeNames(t, ctx, fixture)
	})
	t.Run("RefusesAnUnknownGroup", func(t *testing.T) {
		conformance.RunCounterRefusesAnUnknownGroup(t, ctx, fixture)
	})
	t.Run("NormalizesLabelsAndLeavesTheRequestAlone", func(t *testing.T) {
		conformance.RunCounterNormalizesLabelsAndLeavesTheRequestAlone(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunCounterWritesNothing(t, ctx, fixture)
	})
	t.Run("ParentIDScopesToChildren", func(t *testing.T) {
		conformance.RunCounterParentIDScopesToChildren(t, ctx, fixture)
	})
	t.Run("NoParentExcludesChildren", func(t *testing.T) {
		conformance.RunCounterNoParentExcludesChildren(t, ctx, fixture)
	})
	t.Run("ExcludeTypesNarrowsThePredicate", func(t *testing.T) {
		conformance.RunCounterExcludeTypesNarrowsThePredicate(t, ctx, fixture)
	})
	t.Run("ExcludeStatusNarrowsThePredicate", func(t *testing.T) {
		conformance.RunCounterExcludeStatusNarrowsThePredicate(t, ctx, fixture)
	})
	t.Run("ParentIDMatchesListCardinality", func(t *testing.T) {
		conformance.RunCounterParentIDMatchesListCardinality(t, ctx, fixture)
	})
	t.Run("NoParentMatchesListCardinality", func(t *testing.T) {
		conformance.RunCounterNoParentMatchesListCardinality(t, ctx, fixture)
	})
	t.Run("ExcludeTypesMatchesListCardinality", func(t *testing.T) {
		conformance.RunCounterExcludeTypesMatchesListCardinality(t, ctx, fixture)
	})
	t.Run("ParentIDIncludesAWispChild", func(t *testing.T) {
		conformance.RunCounterParentIDIncludesAWispChild(t, ctx, fixture)
	})
	t.Run("ParentIDAndExcludeStatusComposeOnAClosedChild", func(t *testing.T) {
		conformance.RunCounterParentIDAndExcludeStatusComposeOnAClosedChild(t, ctx, fixture)
	})
}

func newEmbeddedCounterFixture(t *testing.T, te *testEnv, prefix string) conformance.CounterFixture {
	t.Helper()
	counter, err := te.store.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.CounterFixture{
		IssuePrefix:   kit.IssuePrefix,
		Counter:       counter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		CountHistory:  kit.CountHistory,
		AddDependency: kit.AddDependency,
		List:          reader.List,
	}
}

// TestCounterExcludeStatusAcceptsAWorkspaceCustomStatusWithoutIncludeInfra
// pins the PR #7199 review's blocker fix: the store-backed Counter must load
// the workspace's list configuration when ExcludeStatus is non-empty, not
// only when IncludeInfra is set, so BuildCountFilter's validation sees the
// workspace's own custom statuses. A leg-level test, not a conformance case,
// because CounterFixture cannot drive a workspace-configured custom status
// (counter_contract.go's CounterFixture doc) — this needs a real store with
// status.custom set.
//
// Reproduces the reviewer's CLI repro directly against the store: with
// status.custom="review,qa", an issue moved to "review", and IncludeInfra
// left unset, `--exclude-status review` must narrow the count instead of
// being refused as an unrecognized status.
func TestCounterExcludeStatusAcceptsAWorkspaceCustomStatusWithoutIncludeInfra(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cntcs")
	ctx := t.Context()

	if err := te.store.SetConfig(ctx, "status.custom", "review,qa"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}

	const openID = "cntcs-open"
	const reviewID = "cntcs-review"
	for _, id := range []string{openID, reviewID} {
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	if err := te.store.UpdateIssue(ctx, reviewID, map[string]interface{}{
		"status": types.Status("review"),
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(%s) to review: %v", reviewID, err)
	}

	counter, err := te.store.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}

	scope := publicops.CountRequest{IDFilter: openID + "," + reviewID}
	total, err := counter.Count(ctx, scope)
	if err != nil {
		t.Fatalf("Count(unfiltered): %v", err)
	}
	if total.Total != 2 {
		t.Fatalf("Count(unfiltered).Total = %d, want 2", total.Total)
	}

	// IncludeInfra left UNSET: before the fix this refused with "invalid
	// exclude-status \"review\"" because the config load was gated on
	// IncludeInfra alone.
	scope.ExcludeStatus = []string{"review"}
	result, err := counter.Count(ctx, scope)
	if err != nil {
		t.Fatalf("Count(exclude-status=review, IncludeInfra unset): %v, want the custom status accepted", err)
	}
	if result.Total != 1 {
		t.Fatalf("Count(exclude-status=review).Total = %d, want 1", result.Total)
	}
}
