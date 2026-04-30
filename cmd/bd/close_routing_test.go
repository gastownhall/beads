//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestResolveCloseTargets_RoutesToContributorPlanning is the regression test
// for beads-0km: bd close failed for issues that lived only in the routed
// contributor planning store, while bd show / bd update succeeded for the
// same IDs because they routed via resolveAndGetIssueWithRouting and close
// did not. The fix uses resolveCloseTargets, which now performs the same
// routing fallback (and shares one routed-store handle across IDs to avoid
// the GH#3586 flock-deadlock pattern when multiple IDs route to one target).
func TestResolveCloseTargets_RoutesToContributorPlanning(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "contributor")

	primaryStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "shared")
	ctx := context.Background()

	if err := primaryStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("set routing.mode: %v", err)
	}
	if err := primaryStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
		t.Fatalf("set routing.contributor: %v", err)
	}

	// Planning store with two issues that share the primary's prefix —
	// the contributor-routing layout `bd create` produces in this scenario.
	planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "shared")
	for _, id := range []string{"shared-aaa", "shared-bbb"} {
		issue := &types.Issue{
			ID:        id,
			Title:     "planning issue " + id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := planningStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("seed planning issue %s: %v", id, err)
		}
	}
	// Release the lock so openRoutedReadStore can acquire it.
	if err := planningStore.Close(); err != nil {
		t.Fatalf("close planning store: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir repoDir: %v", err)
	}

	// Multi-id batch: this is the deadlock-prone case. resolveCloseTargets
	// must open the routed store at most once and reuse it across both IDs.
	results, cleanup, err := resolveCloseTargets(ctx, primaryStore, []string{"shared-aaa", "shared-bbb"})
	if err != nil {
		t.Fatalf("resolveCloseTargets: %v", err)
	}
	defer cleanup()

	if got, want := len(results), 2; got != want {
		t.Fatalf("got %d results, want %d", got, want)
	}
	wantIDs := []string{"shared-aaa", "shared-bbb"}
	for i, r := range results {
		if r == nil || r.Issue == nil {
			t.Fatalf("result[%d] missing Issue", i)
		}
		if r.ResolvedID != wantIDs[i] {
			t.Errorf("result[%d].ResolvedID = %q, want %q", i, r.ResolvedID, wantIDs[i])
		}
		if !r.Routed {
			t.Errorf("result[%d].Routed = false, want true (issue lives in planning store)", i)
		}
		if r.Store == nil {
			t.Errorf("result[%d].Store is nil", i)
		}
	}
	// Both results must point at the same routed store handle — the dedupe
	// is what prevents the flock deadlock.
	if results[0].Store != results[1].Store {
		t.Error("both results should share one routed store handle to avoid flock deadlock")
	}

	// Sanity: the close path can mutate via that store.
	if err := results[0].Store.CloseIssue(ctx, results[0].ResolvedID, "Closed", "test", ""); err != nil {
		t.Fatalf("CloseIssue via routed store failed: %v", err)
	}
	closed, err := results[0].Store.GetIssue(ctx, results[0].ResolvedID)
	if err != nil {
		t.Fatalf("re-read closed issue: %v", err)
	}
	if closed.Status != types.StatusClosed {
		t.Errorf("issue status = %q, want %q", closed.Status, types.StatusClosed)
	}
}

// TestResolveCloseTargets_LocalStoreUnchanged guards the maintainer-mode
// (no-routing) path: when an issue lives in the local store, resolveCloseTargets
// must resolve it without ever opening a routed store.
func TestResolveCloseTargets_LocalStoreUnchanged(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "maintainer")

	localStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "shared")
	ctx := context.Background()

	issue := &types.Issue{
		ID:        "shared-local",
		Title:     "local-only issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := localStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("seed local issue: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir repoDir: %v", err)
	}

	results, cleanup, err := resolveCloseTargets(ctx, localStore, []string{"shared-local"})
	if err != nil {
		t.Fatalf("resolveCloseTargets: %v", err)
	}
	defer cleanup()

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Routed {
		t.Error("results[0].Routed = true, want false (issue is local)")
	}
	if results[0].Store != localStore {
		t.Error("results[0].Store should be the local store for non-routed lookup")
	}
}
