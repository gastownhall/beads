package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestCounterContract runs the Counter contract against the unit-of-work
// provider — the one Counter implementation that does not hand back
// internal/workapi/storecounter. The two store backends share that body between
// them, which makes this the SECOND of two votes rather than the third.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so dolt_log and the issues table are database-global
// and a parallel subtest would corrupt another subtest's history delta.
func TestCounterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWCounterFixture(t, ctx, "cnt")

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

func newUOWCounterFixture(t *testing.T, ctx context.Context, prefix string) conformance.CounterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewCounter: a provider that stopped
	// offering the role is the regression, and a constructor call would hide it.
	source, ok := provider.(CounterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Counter accessor", provider)
	}
	counter, err := source.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	readerSource, ok := provider.(IssueReaderSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueReader accessor", provider)
	}
	reader, err := readerSource.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
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
// pins the PR #7199 review's blocker fix on the unit-of-work leg: countFilter
// must load the workspace's list configuration when ExcludeStatus is
// non-empty, not only when IncludeInfra is set, so BuildCountFilter's
// validation sees the workspace's own custom statuses. A leg-level test, not
// a conformance case, because CounterFixture cannot drive a
// workspace-configured custom status (counter_contract.go's CounterFixture
// doc) — this needs a real provider with status.custom set.
//
// Its own provider (not newUOWCounterFixture's), so status.custom does not
// leak into the other subtests sharing that fixture's one database.
func TestCounterExcludeStatusAcceptsAWorkspaceCustomStatusWithoutIncludeInfra(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "cntcs")

	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "set status.custom", uw.ConfigUseCase().SetConfig(ctx, "status.custom", "review,qa")
	}); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}

	const openID = "cntcs-open"
	const reviewID = "cntcs-review"
	for _, id := range []string{openID, reviewID} {
		id := id
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
				Issue: &types.Issue{
					ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
				},
				ExplicitID: id,
				CreateOnly: true,
			}, "tester")
			return "seed " + id, err
		}); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "move to review", uw.IssueUseCase().UpdateIssue(ctx, reviewID, map[string]any{
			"status": types.Status("review"),
		}, "tester")
	}); err != nil {
		t.Fatalf("UpdateIssue(%s) to review: %v", reviewID, err)
	}

	source, ok := provider.(CounterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Counter accessor", provider)
	}
	counter, err := source.Counter()
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

// TestCounterInfraTypeCountMatchesList pins GH#6258 end to end on the
// unit-of-work leg — the body behind proxied and served counts. The embedded
// leg's twin (embeddeddolt/counter_contract_test.go) carries the full
// commentary; this one proves the same facts through countFilter's config
// load: with types.infra=robot, a plain typed count must recognize "robot" as
// infra (workapi.CountNeedsConfig) and count the same rows the reader lists
// for that type, through both Count and CountByGroup.
//
// Its own provider, like the ExcludeStatus test above: types.custom and
// types.infra are workspace-global, so they must not leak into the suite
// sharing newUOWCounterFixture's one database.
func TestCounterInfraTypeCountMatchesList(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "cntinf")

	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, "types.custom", "robot,agent"); err != nil {
			return "", err
		}
		return "set types.infra", uw.ConfigUseCase().SetConfig(ctx, "types.infra", "robot")
	}); err != nil {
		t.Fatalf("SetConfig(types): %v", err)
	}

	// Two robot wisps (the plane a listing of an infra type reads), one durable
	// task retyped to robot after creation (update does not run create-time
	// routing), and one durable agent (not infra in this workspace).
	seed := func(wisp bool, id string, issueType types.IssueType) error {
		return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			issue := &types.Issue{
				ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: issueType,
				Ephemeral: wisp,
			}
			params := domain.CreateIssueParams{Issue: issue, ExplicitID: id, CreateOnly: true}
			var err error
			if wisp {
				_, err = uw.IssueUseCase().CreateWisp(ctx, params, "tester")
			} else {
				_, err = uw.IssueUseCase().CreateIssue(ctx, params, "tester")
			}
			return "seed " + id, err
		})
	}
	for _, w := range []struct {
		id        string
		issueType types.IssueType
		wisp      bool
	}{
		{"cntinf-r1", "robot", true},
		{"cntinf-r2", "robot", true},
		{"cntinf-r3", types.TypeTask, false},
		{"cntinf-a1", "agent", false},
	} {
		if err := seed(w.wisp, w.id, w.issueType); err != nil {
			t.Fatalf("seed(%s): %v", w.id, err)
		}
	}
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "retype to robot", uw.IssueUseCase().UpdateIssue(ctx, "cntinf-r3", map[string]any{
			"issue_type": "robot",
		}, "tester")
	}); err != nil {
		t.Fatalf("UpdateIssue(cntinf-r3) to robot: %v", err)
	}

	source, ok := provider.(CounterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Counter accessor", provider)
	}
	counter, err := source.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	readerSource, ok := provider.(IssueReaderSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueReader accessor", provider)
	}
	reader, err := readerSource.IssueReader()
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
		{"robot", false, false, 2},
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
			if grouped.Total != int64(len(page.Items)) {
				t.Errorf("CountByGroup(%s).Total = %d, want len(List(%s)) = %d", name, grouped.Total, name, len(page.Items))
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

	// The untyped count keeps its durable-plane answer: the retyped robot and
	// the durable agent.
	untyped, err := counter.Count(ctx, publicops.CountRequest{IDFilter: scope})
	if err != nil {
		t.Fatalf("Count(untyped): %v", err)
	}
	if untyped.Total != 2 {
		t.Errorf("Count(untyped) = %d, want 2 (the durable rows only)", untyped.Total)
	}

	// So an infra type's --by-type bucket is NOT its typed count (the
	// embedded twin explains the split): by default robot's bucket is the
	// retyped durable robot alone, the typed count the two robot wisps, and
	// only IncludeEphemeral makes them agree.
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
