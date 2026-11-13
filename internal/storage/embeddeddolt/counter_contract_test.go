//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"fmt"
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

// TestCounterInfraTypeCountMatchesList pins GH#6258 end to end on the embedded
// store: `bd count --type <infra>` must count the same rows `bd list --type
// <infra>` shows — the type's ephemeral wisps-tier rows — for every
// combination of --include-ephemeral and --include-infra. Recognizing the type
// as infra requires the workspace's types.infra, so this is also the test that
// pins the counter's config load: with types.infra=robot, a plain typed count
// must load the config (workapi.CountNeedsConfig) or "robot" would never be
// treated as infra and the count would read the durable plane alone.
//
// The seed is the bug report's shape: two robot wisps (the plane a listing of
// an infra type reads), one durable task retyped to robot (create-time routing
// does not run on UpdateIssue, so the row stays durable), and one durable
// agent (agent is NOT infra in this workspace, so it is durable work counted
// on every arm). A leg-level test, not a conformance case, for the same
// reason the ExcludeStatus test above is: CounterFixture cannot drive a
// workspace-configured types.infra.
func TestCounterInfraTypeCountMatchesList(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cntinf")
	ctx := t.Context()

	if err := te.store.SetConfig(ctx, "types.custom", "robot,agent"); err != nil {
		t.Fatalf("SetConfig(types.custom): %v", err)
	}
	if err := te.store.SetConfig(ctx, "types.infra", "robot"); err != nil {
		t.Fatalf("SetConfig(types.infra): %v", err)
	}

	// Two robot wisps: the ephemeral rows a listing of the infra type reads.
	for _, id := range []string{"cntinf-r1", "cntinf-r2"} {
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: "robot", Ephemeral: true,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	// One durable task, retyped to robot after creation: update does not run
	// create-time routing, so the row stays in the durable plane.
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: "cntinf-r3", Title: "cntinf-r3", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue(cntinf-r3): %v", err)
	}
	if err := te.store.UpdateIssue(ctx, "cntinf-r3", map[string]interface{}{
		"issue_type": "robot",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(cntinf-r3) to robot: %v", err)
	}
	// One durable agent: not infra in this workspace.
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: "cntinf-a1", Title: "cntinf-a1", Status: types.StatusOpen, Priority: 2, IssueType: "agent",
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue(cntinf-a1): %v", err)
	}

	counter, err := te.store.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}

	const scope = "cntinf-r1,cntinf-r2,cntinf-r3,cntinf-a1"
	for _, tt := range []struct {
		issueType string
		ephemeral bool
		infra     bool
		want      int64
	}{
		{"robot", false, false, 2}, // the bug: main counted 1 here
		{"robot", true, false, 3},
		{"robot", false, true, 2},
		{"robot", true, true, 3},
		{"agent", false, false, 1},
		{"agent", true, false, 1},
		{"agent", false, true, 1},
		{"agent", true, true, 1},
	} {
		name := fmt.Sprintf("%s/ephemeral=%v/infra=%v", tt.issueType, tt.ephemeral, tt.infra)
		t.Run(name, func(t *testing.T) {
			countReq := publicops.CountRequest{
				IssueType: tt.issueType, IncludeEphemeral: tt.ephemeral, IncludeInfra: tt.infra, IDFilter: scope,
			}
			result, err := counter.Count(ctx, countReq)
			if err != nil {
				t.Fatalf("Count(%s): %v", name, err)
			}
			// The listing twin: AllFlag and IncludeTemplates lift the
			// deliberate count-versus-list differences (closed/pinned rows,
			// templates) that are not this bug; the plane is what must agree.
			page, err := reader.List(ctx, publicops.ListRequest{
				IssueType: tt.issueType, IncludeEphemeral: tt.ephemeral, IncludeInfra: tt.infra,
				AllFlag: true, IncludeTemplates: true, IDFilter: scope,
			})
			if err != nil {
				t.Fatalf("List(%s): %v", name, err)
			}
			if result.Total != int64(len(page.Items)) {
				t.Errorf("Count(%s) = %d, len(List(%s)) = %d, want equal",
					name, result.Total, name, len(page.Items))
			}
			if result.Total != tt.want {
				t.Errorf("Count(%s) = %d, want %d", name, result.Total, tt.want)
			}

			grouped, err := counter.CountByGroup(ctx, publicops.CountByGroupRequest{
				Filter: countReq, GroupBy: publicops.CountGroupStatus,
			})
			if err != nil {
				t.Fatalf("CountByGroup(%s): %v", name, err)
			}
			if grouped.Total != result.Total {
				t.Errorf("CountByGroup(%s).Total = %d, want the scalar %d", name, grouped.Total, result.Total)
			}
			// Total re-runs the scalar count, so only the buckets read the
			// grouped query. Status buckets are disjoint: their sum is exact.
			var bucketSum int64
			for _, n := range grouped.Groups {
				bucketSum += int64(n)
			}
			if bucketSum != tt.want {
				t.Errorf("CountByGroup(%s) buckets %v sum to %d, want %d", name, grouped.Groups, bucketSum, tt.want)
			}
		})
	}

	// The untyped count keeps its historical durable-plane answer: the two
	// durable rows in scope (the retyped robot and the agent), not the wisps.
	// It deliberately does NOT equal the default listing, which suppresses
	// infra types (the PR's "noticed, not changed" list).
	untyped, err := counter.Count(ctx, publicops.CountRequest{IDFilter: scope})
	if err != nil {
		t.Fatalf("Count(untyped): %v", err)
	}
	if untyped.Total != 2 {
		t.Errorf("Count(untyped) = %d, want 2 (the durable rows only)", untyped.Total)
	}

	// So an infra type's --by-type bucket is NOT its typed count, and this
	// pins that split rather than leaving it implicit: the grouping is an
	// untyped count, so by default robot's bucket is the retyped durable robot
	// alone, while the typed count reads the two robot wisps. Only
	// IncludeEphemeral makes the two agree, both then counting both planes;
	// IncludeInfra admits the wisps tier to the bucket and keeps the typed
	// count's narrowing.
	for _, tt := range []struct {
		ephemeral  bool
		infra      bool
		wantBucket int
		wantTyped  int64
	}{
		{false, false, 1, 2},
		{true, false, 3, 3},
		{false, true, 3, 2},
	} {
		name := fmt.Sprintf("by-type/ephemeral=%v/infra=%v", tt.ephemeral, tt.infra)
		t.Run(name, func(t *testing.T) {
			byType, err := counter.CountByGroup(ctx, publicops.CountByGroupRequest{
				Filter:  publicops.CountRequest{IncludeEphemeral: tt.ephemeral, IncludeInfra: tt.infra, IDFilter: scope},
				GroupBy: publicops.CountGroupType,
			})
			if err != nil {
				t.Fatalf("CountByGroup(%s): %v", name, err)
			}
			typed, err := counter.Count(ctx, publicops.CountRequest{
				IssueType: "robot", IncludeEphemeral: tt.ephemeral, IncludeInfra: tt.infra, IDFilter: scope,
			})
			if err != nil {
				t.Fatalf("Count(robot, %s): %v", name, err)
			}
			if got := byType.Groups["robot"]; got != tt.wantBucket || typed.Total != tt.wantTyped {
				t.Errorf("%s: robot bucket = %d (groups %v), --type robot = %d; want %d and %d",
					name, got, byType.Groups, typed.Total, tt.wantBucket, tt.wantTyped)
			}
		})
	}
}

// TestReaderListExcludeStatusAcceptsAWorkspaceCustomStatus is the listing's
// twin of the test above: ListRequest.ExcludeStatus shares the count's
// validation (workapi.ParseExcludeStatus) and must see the workspace's own
// custom statuses, so a custom status is accepted and narrows the listing,
// and a name in no vocabulary is refused. A leg-level test for the same
// reason: ReaderFixture cannot configure a workspace custom status.
func TestReaderListExcludeStatusAcceptsAWorkspaceCustomStatus(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "lstcs")
	ctx := t.Context()

	if err := te.store.SetConfig(ctx, "status.custom", "review,qa"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}
	const openID, reviewID = "lstcs-open", "lstcs-review"
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
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}

	scope := publicops.ListRequest{IDFilter: openID + "," + reviewID, ExcludeStatus: []string{"review"}}
	page, err := reader.List(ctx, scope)
	if err != nil {
		t.Fatalf("List(ExcludeStatus=review): %v, want the custom status accepted", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != openID {
		ids := make([]string, 0, len(page.Items))
		for _, it := range page.Items {
			ids = append(ids, it.ID)
		}
		t.Fatalf("List(ExcludeStatus=review) = %v, want [%s]", ids, openID)
	}

	scope.ExcludeStatus = []string{"reveiw"}
	if _, err := reader.List(ctx, scope); !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("List(ExcludeStatus=reveiw) = %v, want ErrValidation", err)
	}
}
