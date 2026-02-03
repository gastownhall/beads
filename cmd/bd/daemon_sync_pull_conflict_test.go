//go:build integration
// +build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestSyncBranchPull_DivergedHistory tests that daemon pull handles diverged sync branches.
// This simulates the scenario from GH#1358 where:
// 1. Two developers both make changes while daemon is running
// 2. Developer A's daemon pushes first (succeeds)
// 3. Developer B's daemon tries to push - fails due to divergence, leaves worktree dirty
// 4. Developer B's daemon tries to pull on next cycle - FAILS with "unmerged files" error
//
// This test should FAIL with old code (simple git pull) and PASS with fix (PullFromSyncBranch).
func TestSyncBranchPull_DivergedHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create bare remote repository
	tmpDir := t.TempDir()
	remoteDir := filepath.Join(tmpDir, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("Failed to create remote dir: %v", err)
	}
	runGitCmd(t, remoteDir, "init", "--bare", "-b", "master")

	syncBranch := "beads-sync"

	// === Developer A Setup ===
	devADir := filepath.Join(tmpDir, "devA")
	runGitCmd(t, tmpDir, "clone", remoteDir, devADir)
	configureGit(t, devADir)

	devABeadsDir := filepath.Join(devADir, ".beads")
	if err := os.MkdirAll(devABeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create devA .beads dir: %v", err)
	}

	devADBPath := filepath.Join(devABeadsDir, "test.db")
	storeA, err := sqlite.New(ctx, devADBPath)
	if err != nil {
		t.Fatalf("Failed to create storeA: %v", err)
	}
	defer storeA.Close()

	if err := storeA.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix for A: %v", err)
	}
	if err := storeA.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch for A: %v", err)
	}

	// Create and push initial state
	issueA1 := &types.Issue{
		Title:     "Initial issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := storeA.CreateIssue(ctx, issueA1, "devA"); err != nil {
		t.Fatalf("Failed to create issue in devA: %v", err)
	}

	devAJSONLPath := filepath.Join(devABeadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, storeA, devAJSONLPath); err != nil {
		t.Fatalf("Failed to export for devA: %v", err)
	}

	initMainBranch(t, devADir)
	runGitCmd(t, devADir, "push", "origin", "master")

	if err := os.Chdir(devADir); err != nil {
		t.Fatalf("Failed to chdir to devA: %v", err)
	}
	oldDBPath := dbPath
	dbPath = devADBPath
	logA, _ := newTestSyncBranchLogger()
	if _, err := syncBranchCommitAndPush(ctx, storeA, true, logA); err != nil {
		t.Fatalf("DevA initial push failed: %v", err)
	}
	dbPath = oldDBPath
	t.Log("✓ DevA pushed initial state")

	git.ResetCaches()

	// === Developer B Setup ===
	devBDir := filepath.Join(tmpDir, "devB")
	runGitCmd(t, tmpDir, "clone", remoteDir, devBDir)
	configureGit(t, devBDir)

	devBBeadsDir := filepath.Join(devBDir, ".beads")
	if err := os.MkdirAll(devBBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create devB .beads dir: %v", err)
	}

	devBDBPath := filepath.Join(devBBeadsDir, "test.db")
	storeB, err := sqlite.New(ctx, devBDBPath)
	if err != nil {
		t.Fatalf("Failed to create storeB: %v", err)
	}
	defer storeB.Close()

	if err := storeB.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix for B: %v", err)
	}
	if err := storeB.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch for B: %v", err)
	}

	// DevB pulls initial state
	if err := os.Chdir(devBDir); err != nil {
		t.Fatalf("Failed to chdir to devB: %v", err)
	}
	dbPath = devBDBPath
	logB, _ := newTestSyncBranchLogger()
	if _, err := syncBranchPull(ctx, storeB, logB); err != nil {
		t.Logf("DevB initial pull warning: %v", err)
	}
	dbPath = oldDBPath
	t.Log("✓ DevB pulled initial state")

	git.ResetCaches()

	// === SIMULATE CONCURRENT CHANGES (the key to reproducing GH#1358) ===

	// DevA creates issue (simulating daemon auto-export)
	if err := os.Chdir(devADir); err != nil {
		t.Fatalf("Failed to chdir to devA: %v", err)
	}
	issueA2 := &types.Issue{
		Title:     "DevA change",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
		CreatedAt: time.Now().Add(time.Second),
		UpdatedAt: time.Now().Add(time.Second),
	}
	if err := storeA.CreateIssue(ctx, issueA2, "devA"); err != nil {
		t.Fatalf("Failed to create issue in devA: %v", err)
	}
	if err := exportToJSONLWithStore(ctx, storeA, devAJSONLPath); err != nil {
		t.Fatalf("Failed to export for devA: %v", err)
	}

	// DevA daemon pushes (succeeds - first to push)
	dbPath = devADBPath
	if _, err := syncBranchCommitAndPush(ctx, storeA, true, logA); err != nil {
		t.Fatalf("DevA push failed: %v", err)
	}
	dbPath = oldDBPath
	t.Log("✓ DevA pushed change (remote now ahead of DevB)")

	git.ResetCaches()

	// DevB creates DIFFERENT issue (simulating daemon auto-export, without pulling first)
	if err := os.Chdir(devBDir); err != nil {
		t.Fatalf("Failed to chdir to devB: %v", err)
	}
	issueB := &types.Issue{
		Title:     "DevB change",
		Status:    types.StatusInProgress,
		Priority:  1,
		IssueType: types.TypeFeature,
		CreatedAt: time.Now().Add(2 * time.Second),
		UpdatedAt: time.Now().Add(2 * time.Second),
	}
	if err := storeB.CreateIssue(ctx, issueB, "devB"); err != nil {
		t.Fatalf("Failed to create issue in devB: %v", err)
	}
	devBJSONLPath := filepath.Join(devBBeadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, storeB, devBJSONLPath); err != nil {
		t.Fatalf("Failed to export for devB: %v", err)
	}

	// DevB daemon tries to push - this will FAIL and potentially leave worktree dirty
	dbPath = devBDBPath
	_, pushErr := syncBranchCommitAndPush(ctx, storeB, true, logB)
	dbPath = oldDBPath
	if pushErr != nil {
		t.Logf("✓ DevB push failed as expected (divergence): %v", pushErr)
	} else {
		t.Log("DevB push succeeded (may have auto-rebased)")
	}

	git.ResetCaches()

	// === THE CRITICAL TEST: DevB daemon tries to PULL on next cycle ===
	// With OLD code (simple git pull): This FAILS with "unmerged files"
	// With FIX (PullFromSyncBranch): This SUCCEEDS with content-based merge

	if err := os.Chdir(devBDir); err != nil {
		t.Fatalf("Failed to chdir to devB: %v", err)
	}

	dbPath = devBDBPath
	pulled, pullErr := syncBranchPull(ctx, storeB, logB)
	dbPath = oldDBPath

	if pullErr != nil {
		if strings.Contains(pullErr.Error(), "unmerged files") ||
			strings.Contains(pullErr.Error(), "Pulling is not possible") {
			t.Fatalf("❌ GH#1358 REPRODUCED: Daemon pull failed with unmerged files error: %v", pullErr)
		} else {
			t.Errorf("DevB pull failed with unexpected error: %v", pullErr)
		}
	} else {
		t.Log("✅ GH#1358 FIXED: DevB pull succeeded despite divergence")
		if pulled {
			t.Log("✅ Content-based merge handled divergence successfully")
		}
		// Test PASSES - fix is working!
	}

	git.ResetCaches()
}
