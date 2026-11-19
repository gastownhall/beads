package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
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

// TestCrossPrefixBlockerIsNamedByInspectionAndGuardsMutations pins AC-5
// (GH#4647): the blocked inspection names a resolved open cross-prefix
// blocker, IsBlocked and its batch twin report it, the guarded close refuses
// without --force naming the blocker, and --force (or the target closing)
// lets the close through.
func TestCrossPrefixBlockerIsNamedByInspectionAndGuardsMutations(t *testing.T) {
	a := issue("be-a")
	raw := &fakeStore{
		ready: []*types.Issue{a},
		deps: map[string][]*types.Dependency{
			"be-a": {crossPrefixDep("be-a", "gt-target", types.DepBlocks)},
		},
	}
	router := &fakeIssueRouter{statuses: map[string]RoutedIssue{
		"gt-target": {Verdict: IssueRouteFound, Status: types.StatusOpen},
	}}
	store := testStoreWithRouter(raw, router)

	// Inspection: the blocked listing names the routed blocker.
	rows, err := store.GetBlockedIssues(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if got := blockedIssueIDs(rows); !slices.Equal(got, []string{"be-a"}) {
		t.Fatalf("blocked listing = %v, want [be-a]", got)
	}
	if len(rows) != 1 || !slices.Contains(rows[0].BlockedBy, "gt-target") {
		t.Fatalf("blocked row blockers = %v, want gt-target among them", rows)
	}

	// IsBlocked reports the routed blocker by name, alone and in batch.
	blocked, blockers, err := store.IsBlocked(t.Context(), "be-a")
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked || !slices.Contains(blockers, "gt-target") {
		t.Fatalf("IsBlocked = (%v, %v), want blocked with gt-target named", blocked, blockers)
	}
	batch, err := store.IsBlockedBatch(t.Context(), []string{"be-a"})
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	if !batch["be-a"] {
		t.Fatalf("IsBlockedBatch = %v, want be-a blocked", batch)
	}

	// The guarded close refuses without force, naming the routed blocker.
	_, err = store.CloseIssueChecked(t.Context(), "be-a", "closer", storage.CloseIssueOptions{})
	if !errors.Is(err, publicops.ErrCloseBlocked) {
		t.Fatalf("close without force over a routed blocker = %v, want ErrCloseBlocked", err)
	}
	var blockedErr *publicops.BlockedError
	if !errors.As(err, &blockedErr) {
		t.Fatalf("close refusal = %v, want a BlockedError", err)
	}
	if !slices.ContainsFunc(blockedErr.Blockers, func(b publicops.Blocker) bool { return b.ID == "gt-target" }) {
		t.Fatalf("close refusal blockers = %v, want gt-target among them", blockedErr.Blockers)
	}
	if slices.Contains(raw.closed, "be-a") {
		t.Fatal("the blocked issue was closed by the refused call")
	}

	// Force closes it anyway.
	if _, err := store.CloseIssueChecked(t.Context(), "be-a", "closer", storage.CloseIssueOptions{Force: true}); err != nil {
		t.Fatalf("close --force over a routed blocker: %v", err)
	}
	if !slices.Contains(raw.closed, "be-a") {
		t.Fatal("close --force did not reach the inner store")
	}

	// Once the routed target closes, the same guarded close goes through
	// without force on a fresh open source.
	a.Status = types.StatusOpen
	router.statuses["gt-target"] = RoutedIssue{Verdict: IssueRouteFound, Status: types.StatusClosed}
	if blocked, _, err := store.IsBlocked(t.Context(), "be-a"); err != nil {
		t.Fatalf("IsBlocked after the target closed: %v", err)
	} else if blocked {
		t.Fatal("a closed routed target still blocks")
	}
	if _, err := store.CloseIssueChecked(t.Context(), "be-a", "closer", storage.CloseIssueOptions{}); err != nil {
		t.Fatalf("close without force after the target closed: %v", err)
	}
}

// TestCrossPrefixNonBlockingEdgeTypesDoNotExclude pins AC-6 (GH#4647): only
// blocks and conditional-blocks make a bare cross-prefix target a blocker. A
// waits-for or relates-to edge onto another rig's open issue leaves its
// source ready and unblocked — the same single classification the ready
// evaluation, the blocked inspection and the close guard share, so no
// collector may treat these edges as blockers.
func TestCrossPrefixNonBlockingEdgeTypesDoNotExclude(t *testing.T) {
	for _, tc := range []struct {
		name    string
		depType types.DependencyType
	}{
		{name: "waits-for", depType: types.DepWaitsFor},
		{name: "relates-to", depType: types.DepRelatesTo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := issue("be-a"), issue("be-b")
			raw := &fakeStore{
				ready: []*types.Issue{a, b},
				deps: map[string][]*types.Dependency{
					"be-a": {crossPrefixDep("be-a", "gt-target", tc.depType)},
				},
			}
			router := &fakeIssueRouter{statuses: map[string]RoutedIssue{
				"gt-target": {Verdict: IssueRouteFound, Status: types.StatusOpen},
			}}
			store := testStoreWithRouter(raw, router)

			if got := readyIDList(t, store, types.WorkFilter{}); !slices.Equal(got, []string{"be-a", "be-b"}) {
				t.Fatalf("ready IDs with an open bare %s cross-prefix edge = %v, want both sources", tc.name, got)
			}
			if n, err := store.CountReadyWork(t.Context(), types.WorkFilter{}); err != nil {
				t.Fatalf("CountReadyWork: %v", err)
			} else if n != 2 {
				t.Fatalf("ready count with an open bare %s cross-prefix edge = %d, want 2", tc.name, n)
			}
			if blocked, blockers, err := store.IsBlocked(t.Context(), "be-a"); err != nil {
				t.Fatalf("IsBlocked: %v", err)
			} else if blocked || len(blockers) > 0 {
				t.Fatalf("IsBlocked over an open bare %s cross-prefix edge = (%v, %v), want unblocked", tc.name, blocked, blockers)
			}
		})
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
