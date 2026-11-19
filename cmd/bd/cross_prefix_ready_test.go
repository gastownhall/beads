//go:build cgo

package main

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestReadyHonorsRoutedCrossPrefixBlocker pins AC-4 of the bd-un3t5j design
// (GH#4647) end to end: a bare cross-prefix blocks edge — the shape
// `bd dep add hq-src gt-blocker` stores in depends_on_external — excludes its
// source from the ready selection (ready, count, claim) while the routed
// target is open, and the next query admits the source once the target
// closes, with no write to the source row (the materialized is_blocked stays
// untouched; the exclusion is read-time policy).
//
// It exercises the real chain: the dolt store's GetExternalBlockingDependencyRecords
// (whose cross-prefix leg the issueops query test pins), the shared
// classification in externaldeps.policyBlockersFor, and the CLI's
// prefix-route router opening the rig read-only.
//
// This test uses os.Chdir and assigns the CLI globals, so it cannot run in
// parallel.
func TestReadyHonorsRoutedCrossPrefixBlocker(t *testing.T) {
	e := setupCrossPrefixRelateEnv(t)
	ctx := e.ctx

	// A second, unencumbered town issue at a LOWER priority (P3) than the
	// blocked source (P2, from the fixture): without it an empty ready answer
	// would pass trivially, and below it proves the claim skips the blocked
	// P2 source and takes what is beneath it.
	if err := e.townStore.CreateIssue(ctx, &types.Issue{
		ID:        "hq-free1",
		Title:     "Unencumbered town work",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create hq-free1: %v", err)
	}

	// The routed blocker, seeded through a writable rig handle that stays
	// open so the same connection closes the blocker later.
	rigRW := openRigWritable(t, e.rigBeadsDir)
	defer rigRW.Close()
	if err := rigRW.CreateIssue(ctx, &types.Issue{
		ID:        "gt-blocker1",
		Title:     "Routed blocker",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create gt-blocker1: %v", err)
	}

	// The cross-prefix blocks edge: hq-src1 depends on gt-blocker1.
	if err := e.townStore.AddDependencyWithOptions(ctx, &types.Dependency{
		IssueID:     "hq-src1",
		DependsOnID: "gt-blocker1",
		Type:        types.DepBlocks,
	}, "seed", storage.DependencyAddOptions{}); err != nil {
		t.Fatalf("seed blocks edge hq-src1 -> gt-blocker1: %v", err)
	}

	// The policy instance the direct CLI composes, router included.
	wired := wireExternalDependencyPolicy(e.townStore, cliIssueRouter{})

	// While the routed target is open, the source is excluded from the ready
	// listing, the count and the claim alike.
	if got := crossPrefixReadyIDs(t, ctx, wired); !slices.Equal(got, []string{"hq-free1"}) {
		t.Fatalf("ready IDs with an open routed blocker = %v, want [hq-free1]", got)
	}
	counter, ok := wired.(storage.ReadyWorkCounter)
	if !ok {
		t.Fatalf("wired policy %T does not implement storage.ReadyWorkCounter", wired)
	}
	if n, err := counter.CountReadyWork(ctx, types.WorkFilter{}); err != nil {
		t.Fatalf("CountReadyWork: %v", err)
	} else if n != 1 {
		t.Fatalf("ready count with an open routed blocker = %d, want 1", n)
	}
	// The P2 source sits ABOVE the P3 free issue, so a claim that honored the
	// blocker takes the free issue; a claim that leaked the blocked source
	// would instead hand out hq-src1 (the top of the unfiltered set).
	claimed, err := wired.ClaimReadyIssue(ctx, types.WorkFilter{}, "claimer")
	if err != nil {
		t.Fatalf("ClaimReadyIssue: %v", err)
	}
	if claimed == nil || claimed.ID != "hq-free1" {
		t.Fatalf("claim picked %v, want hq-free1 (the cross-prefix-blocked P2 source must not be claimed over the P3 free issue)", claimed)
	}

	// Close the foreign blocker and ask again: the SAME source, which no
	// local write has touched, is now ready. is_blocked was never flipped —
	// the exclusion was query-time policy (the design: is_blocked stays
	// local).
	if err := rigRW.CloseIssue(ctx, "gt-blocker1", "blocker done", "test", "test-session"); err != nil {
		t.Fatalf("close gt-blocker1: %v", err)
	}
	// The SAME source, which no local write has touched, is ready again on
	// the next query. (hq-free1 may appear too: a claim records an assignee
	// but never flips the status column the ready query reads.)
	got := crossPrefixReadyIDs(t, ctx, wired)
	if !slices.Contains(got, "hq-src1") {
		t.Fatalf("ready IDs after the routed blocker closed = %v, want hq-src1 among them", got)
	}
	townRO := e.reopenTownReadonly(t)
	src, err := townRO.GetIssue(ctx, "hq-src1")
	if err != nil {
		t.Fatalf("town GetIssue(hq-src1): %v", err)
	}
	if src == nil {
		t.Fatal("town issue hq-src1 disappeared")
	}
	if src.IsBlocked {
		t.Error("hq-src1 is_blocked was flipped by the cross-prefix blocking policy; the materialized flag must stay local")
	}
}

// readyIDs lists the full ready set's ids in sorted order, so set comparisons
// are stable whatever the store's row order is.
func crossPrefixReadyIDs(t *testing.T, ctx context.Context, st storage.DoltStorage) []string {
	t.Helper()
	issues, err := st.GetReadyWork(ctx, types.WorkFilter{})
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

// TestReadyAdmitsUnroutedAndAbsentCrossPrefixTargets pins AC-7 (GH#4647) end
// to end through the real CLI router: a bare cross-prefix blocks edge whose
// target NO route reaches (a prefix routes.jsonl never mentions) and one whose
// route reaches a rig that does not hold the issue neither exclude their
// sources from ready. The router's verdicts — IssueRouteUnrouted and
// IssueRouteAbsent — are compatibility non-blockers.
//
// This test uses os.Chdir and assigns the CLI globals, so it cannot run in
// parallel.
func TestReadyAdmitsUnroutedAndAbsentCrossPrefixTargets(t *testing.T) {
	e := setupCrossPrefixRelateEnv(t)
	ctx := e.ctx

	// A second town source, so the absent-target case gets its own edge.
	if err := e.townStore.CreateIssue(ctx, &types.Issue{
		ID:        "hq-src2",
		Title:     "Second town source",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create hq-src2: %v", err)
	}
	// No route is configured for the xx- prefix anywhere: its target is
	// unrouted. gt-nobody IS routed (the fixture's gt- route) but the rig
	// holds no such issue: absent.
	for _, edge := range []struct{ source, target string }{
		{source: "hq-src1", target: "xx-missing"},
		{source: "hq-src2", target: "gt-nobody"},
	} {
		if err := e.townStore.AddDependencyWithOptions(ctx, &types.Dependency{
			IssueID:     edge.source,
			DependsOnID: edge.target,
			Type:        types.DepBlocks,
		}, "seed", storage.DependencyAddOptions{}); err != nil {
			t.Fatalf("seed blocks edge %s -> %s: %v", edge.source, edge.target, err)
		}
	}

	wired := wireExternalDependencyPolicy(e.townStore, cliIssueRouter{})
	got := crossPrefixReadyIDs(t, ctx, wired)
	if !slices.Contains(got, "hq-src1") || !slices.Contains(got, "hq-src2") {
		t.Fatalf("ready IDs with an unrouted and an absent cross-prefix target = %v, want hq-src1 and hq-src2 among them", got)
	}
}
