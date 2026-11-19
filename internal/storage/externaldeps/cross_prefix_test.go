package externaldeps

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fakeIssueRouter is the IssueRouter stand-in: it answers from a seeded map
// (defaulting to absent), and records each call's ids so the batching the
// design promises — one router call per evaluation, each id once — can be
// asserted.
type fakeIssueRouter struct {
	statuses map[string]RoutedIssue
	err      error
	calls    [][]string
}

func (f *fakeIssueRouter) ResolveIssueStatuses(_ context.Context, ids []string) (map[string]RoutedIssue, error) {
	f.calls = append(f.calls, slices.Clone(ids))
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]RoutedIssue, len(ids))
	for _, id := range ids {
		if routed, ok := f.statuses[id]; ok {
			out[id] = routed
			continue
		}
		out[id] = RoutedIssue{Verdict: IssueRouteAbsent}
	}
	return out, nil
}

// testStoreWithRouter is testStore with a cross-prefix router and no
// capability projects configured — the direct-CLI policy instance shape.
func testStoreWithRouter(raw *fakeStore, router IssueRouter) *Store {
	return New(
		raw,
		func(project ProjectName) (string, bool) { return "", false },
		func(_ context.Context, path string) (storage.DoltStorage, error) {
			panic("cross-prefix tests configure no capability projects; open(" + path + ") is a bug")
		},
		WithIssueRouter(router),
	)
}

func crossPrefixDep(source, target string, depType types.DependencyType) *types.Dependency {
	return &types.Dependency{IssueID: source, DependsOnID: target, Type: depType}
}

// readyIDList sorts the ready listing's ids so set comparisons are stable.
func readyIDList(t *testing.T, store *Store, filter types.WorkFilter) []string {
	t.Helper()
	issues, err := store.GetReadyWork(t.Context(), filter)
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != nil {
			ids = append(ids, issue.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// TestCrossPrefixBlockerExcludesReadyUntilItsTargetCloses pins AC-4 (GH#4647)
// at the policy layer: a bare cross-prefix blocks edge excludes its source
// from ready (list, count, claim) while the routed target is open, the batch
// router is asked once per evaluation, and a closed or pinned target admits
// the source again on the NEXT query with no write to the source.
func TestCrossPrefixBlockerExcludesReadyUntilItsTargetCloses(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			"be-a": {
				crossPrefixDep("be-a", "gt-target", types.DepBlocks),
				// A second source with the SAME routed target: the router
				// must still be asked once for it, not once per source.
				crossPrefixDep("be-a", "gt-share", types.DepConditionalBlocks),
			},
			"be-b": {
				crossPrefixDep("be-b", "gt-share", types.DepBlocks),
			},
		},
	}
	router := &fakeIssueRouter{statuses: map[string]RoutedIssue{
		"gt-target": {Verdict: IssueRouteFound, Status: types.StatusOpen},
		"gt-share":  {Verdict: IssueRouteFound, Status: types.StatusOpen},
	}}
	store := testStoreWithRouter(raw, router)

	if got := readyIDList(t, store, types.WorkFilter{}); len(got) != 0 {
		t.Fatalf("ready IDs with open routed blockers = %v, want none", got)
	}
	if n, err := store.CountReadyWork(t.Context(), types.WorkFilter{}); err != nil {
		t.Fatalf("CountReadyWork: %v", err)
	} else if n != 0 {
		t.Fatalf("ready count with open routed blockers = %d, want 0", n)
	}
	if claimed, err := store.ClaimReadyIssue(t.Context(), types.WorkFilter{}, "claimer"); err != nil {
		t.Fatalf("ClaimReadyIssue: %v", err)
	} else if claimed != nil {
		t.Fatalf("claim picked %s over a fully blocked ready set", claimed.ID)
	}
	// One router call per evaluation so far — list, count, claim — each with
	// both distinct targets once.
	if len(router.calls) != 3 {
		t.Fatalf("router calls after list/count/claim = %d, want 3, one per evaluation: %+v", len(router.calls), router.calls)
	}
	for i, call := range router.calls {
		if !slices.Contains(call, "gt-target") || !slices.Contains(call, "gt-share") || len(call) != 2 {
			t.Fatalf("router call %d = %v, want both distinct targets once per evaluation", i, call)
		}
	}

	// The targets close (or pin): same router, next evaluations admit both
	// sources, and nothing was written to them.
	router.statuses["gt-target"] = RoutedIssue{Verdict: IssueRouteFound, Status: types.StatusClosed}
	router.statuses["gt-share"] = RoutedIssue{Verdict: IssueRouteFound, Status: types.StatusPinned}
	if got := readyIDList(t, store, types.WorkFilter{}); !slices.Equal(got, []string{"be-a", "be-b"}) {
		t.Fatalf("ready IDs after the blockers closed/pinned = %v, want [be-a be-b]", got)
	}
	if n, err := store.CountReadyWork(t.Context(), types.WorkFilter{}); err != nil {
		t.Fatalf("CountReadyWork after close: %v", err)
	} else if n != 2 {
		t.Fatalf("ready count after the blockers closed/pinned = %d, want 2", n)
	}
	// The two post-flip evaluations add exactly two more router calls: one
	// for the listing, one for the count.
	if len(router.calls) != 5 {
		t.Fatalf("router calls after the flip = %d, want 5 (list, count, claim, then list, count again): %+v", len(router.calls), router.calls)
	}
	for _, iss := range []*types.Issue{a, b} {
		if iss.IsBlocked {
			t.Errorf("%s was written by the policy; the read-time exclusion must not touch the source row", iss.ID)
		}
	}
}

// TestRouterlessPolicyInstanceHasLegacyCrossPrefixBehavior documents the
// staged boundary: a policy instance with NO router (a serve chain, the
// proxied provider) treats bare cross-prefix targets as blockers it cannot
// verify until AC-7 lands its compatibility refinement.
func TestRouterlessPolicyInstanceHasLegacyCrossPrefixBehavior(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			"be-a": {crossPrefixDep("be-a", "gt-target", types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, false)

	if got := readyIDList(t, store, types.WorkFilter{}); !slices.Equal(got, []string{"be-b"}) {
		t.Fatalf("routerless ready IDs = %v, want [be-b] under the fail-closed stage", got)
	}
}
