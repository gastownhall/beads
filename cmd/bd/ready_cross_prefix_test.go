//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestReadyHonorsRoutedCrossPrefixBlocker (GH#4647, AC-4): in the town, hq-src1
// blocks on gt-target1, which lives in the gt- rig routes.jsonl names. The
// command's policy chain follows the route, so hq-src1 is not ready, counted
// or claimable while gt-target1 is open, and is ready again once gt-target1
// is closed — and once it is pinned — with nothing written to the town.
func TestReadyHonorsRoutedCrossPrefixBlocker(t *testing.T) {
	r := setupRelateRigs(t, nil)
	if err := r.town.AddDependency(r.ctx, &types.Dependency{IssueID: "hq-src1", DependsOnID: "gt-target1", Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("add hq-src1 blocks on gt-target1: %v", err)
	}
	policy := wireStorageDecorators(r.town, nil, true, crossPrefixBlockerRouting())

	ready := func() []string {
		t.Helper()
		issues, err := policy.GetReadyWork(r.ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := make([]string, 0, len(issues))
		for _, issue := range issues {
			ids = append(ids, issue.ID)
		}
		return ids
	}
	setRigStatus := func(status types.Status) {
		t.Helper()
		rig, err := openExistingTestDB(t, r.rigDBPath)
		if err != nil {
			t.Fatalf("reopen rig store: %v", err)
		}
		defer rig.Close()
		if err := rig.UpdateIssue(r.ctx, "gt-target1", map[string]interface{}{"status": string(status)}, "test"); err != nil {
			t.Fatalf("set gt-target1 %s: %v", status, err)
		}
	}

	if got := ready(); slices.Contains(got, "hq-src1") {
		t.Fatalf("ready = %v with gt-target1 open; hq-src1 must be excluded", got)
	}
	counter, ok := policy.(interface {
		CountReadyWork(context.Context, types.WorkFilter) (int, error)
	})
	if !ok {
		t.Fatalf("policy chain %T has no CountReadyWork", policy)
	}
	if n, err := counter.CountReadyWork(r.ctx, types.WorkFilter{}); err != nil || n != 0 {
		t.Fatalf("CountReadyWork = %d, %v; want 0", n, err)
	}
	if claimed, err := policy.ClaimReadyIssue(r.ctx, types.WorkFilter{}, "worker"); err != nil || claimed != nil {
		t.Fatalf("ClaimReadyIssue = %+v, %v; want nothing claimable", claimed, err)
	}
	before := sourceRow(t, r.ctx, r.town)

	for _, status := range []types.Status{types.StatusClosed, types.StatusPinned} {
		setRigStatus(status)
		if got := ready(); !slices.Equal(got, []string{"hq-src1"}) {
			t.Fatalf("ready = %v with gt-target1 %s, want [hq-src1]", got, status)
		}
	}
	if after := sourceRow(t, r.ctx, r.town); after != before {
		t.Fatalf("hq-src1 row changed from %+v to %+v; admitting it must not write the source store", before, after)
	}
}

// sourceRowState is what a write to hq-src1 would change.
type sourceRowState struct {
	status    types.Status
	updatedAt string
	edges     int
}

func sourceRow(t *testing.T, ctx context.Context, s *dolt.DoltStore) sourceRowState {
	t.Helper()
	issue, err := s.GetIssue(ctx, "hq-src1")
	if err != nil {
		t.Fatalf("GetIssue(hq-src1): %v", err)
	}
	deps, err := s.GetDependencyRecords(ctx, "hq-src1")
	if err != nil {
		t.Fatalf("GetDependencyRecords(hq-src1): %v", err)
	}
	return sourceRowState{status: issue.Status, updatedAt: issue.UpdatedAt.String(), edges: len(deps)}
}

// crossPrefixReadyIDs lists the IDs policy.GetReadyWork returns, sorted.
func crossPrefixReadyIDs(t *testing.T, ctx context.Context, policy interface {
	GetReadyWork(context.Context, types.WorkFilter) ([]*types.Issue, error)
}) []string {
	t.Helper()
	issues, err := policy.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestReadyAdmitsUnroutedAndAbsentCrossPrefixTargets (GH#4647, AC-7) runs the
// real CLI router end to end: a blocks edge to xx-missing, a prefix
// routes.jsonl never names, and one to gt-nobody, which the gt- route reaches
// but the rig does not hold, leave both sources ready, and neither prints an
// unavailable-route warning.
func TestReadyAdmitsUnroutedAndAbsentCrossPrefixTargets(t *testing.T) {
	r := setupRelateRigs(t, nil)
	seedIssue(t, r.ctx, r.town, "hq-src2")
	for _, edge := range []struct{ source, target string }{
		{source: "hq-src1", target: "xx-missing"},
		{source: "hq-src2", target: "gt-nobody"},
	} {
		if err := r.town.AddDependency(r.ctx, &types.Dependency{IssueID: edge.source, DependsOnID: edge.target, Type: types.DepBlocks}, "test"); err != nil {
			t.Fatalf("add %s blocks on %s: %v", edge.source, edge.target, err)
		}
	}
	policy := wireStorageDecorators(r.town, nil, true, crossPrefixBlockerRouting())

	var got []string
	stderr := captureStderrForBackupLockTest(t, func() {
		got = crossPrefixReadyIDs(t, r.ctx, policy)
	})
	if !slices.Contains(got, "hq-src1") || !slices.Contains(got, "hq-src2") {
		t.Fatalf("ready = %v with an unrouted and an absent cross-prefix target; want hq-src1 and hq-src2", got)
	}
	if strings.Contains(stderr, "unavailable through their route") {
		t.Fatalf("an unrouted or absent target printed a route warning:\n%s", stderr)
	}
}

// TestReadyExcludesSourcesBehindAFailingConfiguredRouteWithOneWarning (GH#4647,
// AC-8) runs the real CLI router end to end: yy- is routed to a .beads
// directory that holds no database, so the route is configured but cannot
// answer. Both town sources blocked on yy-blocker stay out of ready across two
// evaluations, and the policy warns about yy- once, on the first.
func TestReadyExcludesSourcesBehindAFailingConfiguredRouteWithOneWarning(t *testing.T) {
	r := setupRelateRigs(t, nil)
	townRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "deadrig", ".beads"), 0o755); err != nil {
		t.Fatalf("create dead rig .beads: %v", err)
	}
	routes := "{\"prefix\":\"gt-\",\"path\":\"rig\"}\n{\"prefix\":\"yy-\",\"path\":\"deadrig\"}\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	seedIssue(t, r.ctx, r.town, "hq-src2")
	for _, source := range []string{"hq-src1", "hq-src2"} {
		if err := r.town.AddDependency(r.ctx, &types.Dependency{IssueID: source, DependsOnID: "yy-blocker", Type: types.DepBlocks}, "test"); err != nil {
			t.Fatalf("add %s blocks on yy-blocker: %v", source, err)
		}
	}
	policy := wireStorageDecorators(r.town, nil, true, crossPrefixBlockerRouting())
	const warning = `Warning: issues with prefix "yy-" are unavailable through their route`

	for i, wantWarnings := range []int{1, 0} {
		var got []string
		stderr := captureStderrForBackupLockTest(t, func() {
			got = crossPrefixReadyIDs(t, r.ctx, policy)
		})
		if slices.Contains(got, "hq-src1") || slices.Contains(got, "hq-src2") {
			t.Fatalf("evaluation %d: ready = %v behind a failing configured route; want hq-src1 and hq-src2 excluded", i+1, got)
		}
		if n := strings.Count(stderr, warning); n != wantWarnings {
			t.Fatalf("evaluation %d printed %d yy- route warnings, want %d:\n%s", i+1, n, wantWarnings, stderr)
		}
	}
}
