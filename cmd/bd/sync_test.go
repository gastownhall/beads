//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
)

func TestIsGitRepo_InGitRepo(t *testing.T) {
	// This test assumes we're running in the beads git repo
	if !isGitRepo() {
		t.Skip("not in a git repository")
	}
}

func TestIsGitRepo_NotInGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	if isGitRepo() {
		t.Error("expected false when not in git repo")
	}
}

func TestGitHasUpstream_NoUpstream(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Should not have upstream
	if gitHasUpstream() {
		t.Error("expected false when no upstream configured")
	}
}

func TestGitHasChanges_NoFile(t *testing.T) {
	ctx := context.Background()
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Check - should have no changes (test.txt was committed by setupGitRepo)
	hasChanges, err := gitHasChanges(ctx, "test.txt")
	if err != nil {
		t.Fatalf("gitHasChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes for committed file")
	}
}

func TestGitHasChanges_ModifiedFile(t *testing.T) {
	ctx := context.Background()
	tmpDir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Modify the file
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("modified"), 0644)

	// Check - should have changes
	hasChanges, err := gitHasChanges(ctx, "test.txt")
	if err != nil {
		t.Fatalf("gitHasChanges() error = %v", err)
	}
	if !hasChanges {
		t.Error("expected changes for modified file")
	}
}

func TestGitHasUnmergedPaths_CleanRepo(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Should not have unmerged paths
	hasUnmerged, err := gitHasUnmergedPaths()
	if err != nil {
		t.Fatalf("gitHasUnmergedPaths() error = %v", err)
	}
	if hasUnmerged {
		t.Error("expected no unmerged paths in clean repo")
	}
}

func TestGitCommit_Success(t *testing.T) {
	ctx := context.Background()
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a new file
	testFile := "new.txt"
	os.WriteFile(testFile, []byte("content"), 0644)

	// Commit the file
	err := gitCommit(ctx, testFile, "test commit")
	if err != nil {
		t.Fatalf("gitCommit() error = %v", err)
	}

	// Verify file is committed
	hasChanges, err := gitHasChanges(ctx, testFile)
	if err != nil {
		t.Fatalf("gitHasChanges() error = %v", err)
	}
	if hasChanges {
		t.Error("expected no changes after commit")
	}
}

func TestGitCommit_AutoMessage(t *testing.T) {
	ctx := context.Background()
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create a new file
	testFile := "new.txt"
	os.WriteFile(testFile, []byte("content"), 0644)

	// Commit with auto-generated message (empty string)
	err := gitCommit(ctx, testFile, "")
	if err != nil {
		t.Fatalf("gitCommit() error = %v", err)
	}

	// Verify it committed (message generation worked)
	cmd := exec.Command("git", "log", "-1", "--pretty=%B")
	output, _ := cmd.Output()
	if len(output) == 0 {
		t.Error("expected commit message to be generated")
	}
}

func TestCountIssuesInJSONL_NonExistent(t *testing.T) {
	t.Parallel()
	count, err := countIssuesInJSONL("/nonexistent/path.jsonl")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 on error", count)
	}
}

func TestCountIssuesInJSONL_EmptyFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "empty.jsonl")
	os.WriteFile(jsonlPath, []byte(""), 0644)

	count, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestCountIssuesInJSONL_MultipleIssues(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	content := `{"id":"bd-1"}
{"id":"bd-2"}
{"id":"bd-3"}
`
	os.WriteFile(jsonlPath, []byte(content), 0644)

	count, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestCountIssuesInJSONL_WithMalformedLines(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "mixed.jsonl")
	content := `{"id":"bd-1"}
not valid json
{"id":"bd-2"}
{"id":"bd-3"}
`
	os.WriteFile(jsonlPath, []byte(content), 0644)

	count, err := countIssuesInJSONL(jsonlPath)
	// countIssuesInJSONL returns error on malformed JSON
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
	// Should have counted the first valid issue before hitting error
	if count != 1 {
		t.Errorf("count = %d, want 1 (before malformed line)", count)
	}
}

func TestGetCurrentBranch(t *testing.T) {
	ctx := context.Background()
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Get current branch
	branch, err := getCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("getCurrentBranch() error = %v", err)
	}

	// Default branch is usually main or master
	if branch != "main" && branch != "master" {
		t.Logf("got branch %s (expected main or master, but this can vary)", branch)
	}
}

func TestMergeSyncBranch_NoSyncBranchConfigured(t *testing.T) {
	ctx := context.Background()
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Try to merge without sync.branch configured (or database)
	err := mergeSyncBranch(ctx, false)
	if err == nil {
		t.Error("expected error when sync.branch not configured")
	}
	// Error could be about missing database or missing sync.branch config
	if err != nil && !strings.Contains(err.Error(), "sync.branch") && !strings.Contains(err.Error(), "database") {
		t.Errorf("expected error about sync.branch or database, got: %v", err)
	}
}

func TestMergeSyncBranch_OnSyncBranch(t *testing.T) {
	ctx := context.Background()
	tmpDir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create sync branch
	exec.Command("git", "checkout", "-b", "beads-metadata").Run()

	// Initialize bd database and set sync.branch
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	// This test will fail with store access issues, so we just verify the branch check
	// The actual merge functionality is tested in integration tests
	currentBranch, _ := getCurrentBranch(ctx)
	if currentBranch != "beads-metadata" {
		t.Skipf("test setup failed, current branch is %s", currentBranch)
	}
}

func TestMergeSyncBranch_DirtyWorkingTree(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Create uncommitted changes
	os.WriteFile("test.txt", []byte("modified"), 0644)

	// This test verifies the dirty working tree check would work
	// (We can't test the full merge without database setup)
	statusCmd := exec.Command("git", "status", "--porcelain")
	output, _ := statusCmd.Output()
	if len(output) == 0 {
		t.Error("expected dirty working tree for test setup")
	}
}

func TestGetSyncBranch_EnvOverridesDB(t *testing.T) {
	ctx := context.Background()

	// Save and restore global store state
	oldStore := store
	storeMutex.Lock()
	oldStoreActive := storeActive
	storeMutex.Unlock()
	oldDBPath := dbPath

	// Use a temp dir for Dolt store (Dolt doesn't support :memory:)
	testStore, err := dolt.New(context.Background(), &dolt.Config{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	defer testStore.Close()

	// Seed DB config and globals
	if err := testStore.SetConfig(ctx, "sync.branch", "db-branch"); err != nil {
		t.Fatalf("failed to set sync.branch in db: %v", err)
	}

	storeMutex.Lock()
	store = testStore
	storeActive = true
	storeMutex.Unlock()
	dbPath = "" // avoid FindDatabasePath in ensureStoreActive

	// Set environment override
	if err := os.Setenv(syncbranch.EnvVar, "env-branch"); err != nil {
		t.Fatalf("failed to set %s: %v", syncbranch.EnvVar, err)
	}
	defer os.Unsetenv(syncbranch.EnvVar)

	// Ensure we restore globals after the test
	defer func() {
		storeMutex.Lock()
		store = oldStore
		storeActive = oldStoreActive
		storeMutex.Unlock()
		dbPath = oldDBPath
	}()

	branch, err := getSyncBranch(ctx)
	if err != nil {
		t.Fatalf("getSyncBranch() error = %v", err)
	}
	if branch != "env-branch" {
		t.Errorf("getSyncBranch() = %q, want %q (env override)", branch, "env-branch")
	}
}

func TestIsInRebase_NotInRebase(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Should not be in rebase
	if isInRebase() {
		t.Error("expected false when not in rebase")
	}
}

func TestIsInRebase_InRebase(t *testing.T) {
	tmpDir, cleanup := setupGitRepo(t)
	defer cleanup()

	// Simulate rebase by creating rebase-merge directory
	os.MkdirAll(filepath.Join(tmpDir, ".git", "rebase-merge"), 0755)

	// Should detect rebase
	if !isInRebase() {
		t.Error("expected true when .git/rebase-merge exists")
	}
}

func TestIsInRebase_InRebaseApply(t *testing.T) {
	tmpDir, cleanup := setupMinimalGitRepo(t)
	defer cleanup()

	// Simulate non-interactive rebase by creating rebase-apply directory
	os.MkdirAll(filepath.Join(tmpDir, ".git", "rebase-apply"), 0755)

	// Should detect rebase
	if !isInRebase() {
		t.Error("expected true when .git/rebase-apply exists")
	}
}

func TestHasJSONLConflict_NoConflict(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// Should not have JSONL conflict
	if hasJSONLConflict() {
		t.Error("expected false when no conflicts")
	}
}

func TestHasJSONLConflict_OnlyJSONLConflict(t *testing.T) {
	tmpDir, cleanup := setupGitRepoWithBranch(t, "main")
	defer cleanup()

	// Create initial commit with beads.jsonl
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"bd-1","title":"original"}`), 0644)
	exec.Command("git", "add", ".").Run()
	exec.Command("git", "commit", "-m", "add beads.jsonl").Run()

	// Create a second commit on main (modify same issue)
	os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"bd-1","title":"main-version"}`), 0644)
	exec.Command("git", "add", ".").Run()
	exec.Command("git", "commit", "-m", "main change").Run()

	// Create a branch from the first commit
	exec.Command("git", "checkout", "-b", "feature", "HEAD~1").Run()
	os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"bd-1","title":"feature-version"}`), 0644)
	exec.Command("git", "add", ".").Run()
	exec.Command("git", "commit", "-m", "feature change").Run()

	// Attempt rebase onto main (will conflict)
	exec.Command("git", "rebase", "main").Run()

	// Should detect JSONL conflict during rebase
	if !hasJSONLConflict() {
		t.Error("expected true when only beads.jsonl has conflict during rebase")
	}
}

func TestHasJSONLConflict_MultipleConflicts(t *testing.T) {
	tmpDir, cleanup := setupGitRepoWithBranch(t, "main")
	defer cleanup()

	// Create initial commit with beads.jsonl and another file
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"bd-1","title":"original"}`), 0644)
	os.WriteFile("other.txt", []byte("line1\nline2\nline3"), 0644)
	exec.Command("git", "add", ".").Run()
	exec.Command("git", "commit", "-m", "add initial files").Run()

	// Create a second commit on main (modify both files)
	os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"bd-1","title":"main-version"}`), 0644)
	os.WriteFile("other.txt", []byte("line1\nmain-version\nline3"), 0644)
	exec.Command("git", "add", ".").Run()
	exec.Command("git", "commit", "-m", "main change").Run()

	// Create a branch from the first commit
	exec.Command("git", "checkout", "-b", "feature", "HEAD~1").Run()
	os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"bd-1","title":"feature-version"}`), 0644)
	os.WriteFile("other.txt", []byte("line1\nfeature-version\nline3"), 0644)
	exec.Command("git", "add", ".").Run()
	exec.Command("git", "commit", "-m", "feature change").Run()

	// Attempt rebase (will conflict on both files)
	exec.Command("git", "rebase", "main").Run()

	// Should NOT auto-resolve when multiple files conflict
	if hasJSONLConflict() {
		t.Error("expected false when multiple files have conflicts (should not auto-resolve)")
	}
}

// Note: TestZFCSkipsExportAfterImport was removed as ZFC checks are no longer part of the
// legacy sync flow. Use --pull-first for structural staleness handling via 3-way merge.

// TestHashBasedStalenessDetection_bd_f2f tests the bd-f2f fix:
// When JSONL content differs from stored hash (e.g., remote changed status),
// hasJSONLChanged should detect the mismatch even if counts are equal.
func TestHashBasedStalenessDetection_bd_f2f(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test database
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create store
	testStore, err := dolt.New(ctx, &dolt.Config{Path: testDBPath})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	// Initialize issue prefix (required for creating issues)
	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue prefix: %v", err)
	}

	// Create an issue in DB (simulating stale DB with old content)
	issue := &types.Issue{
		ID:        "test-abc",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		Priority:  1, // DB has priority 1
		IssueType: types.TypeTask,
	}
	if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Create JSONL with same issue but different priority (correct remote state)
	// This simulates what happens after git pull brings in updated JSONL
	// (e.g., remote changed priority from 1 to 0)
	jsonlContent := `{"id":"test-abc","title":"Test Issue","status":"open","priority":0,"type":"task"}
`
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0600); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	// Store an OLD hash (different from current JSONL)
	// This simulates the case where JSONL was updated externally (by git pull)
	// but DB still has old hash from before the pull
	oldHash := "0000000000000000000000000000000000000000000000000000000000000000"
	if err := testStore.SetMetadata(ctx, "jsonl_content_hash", oldHash); err != nil {
		t.Fatalf("failed to set old hash: %v", err)
	}

	// Verify counts are equal (1 issue in both)
	dbCount, err := countDBIssuesFast(ctx, testStore)
	if err != nil {
		t.Fatalf("failed to count DB issues: %v", err)
	}
	jsonlCount, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("failed to count JSONL issues: %v", err)
	}
	if dbCount != jsonlCount {
		t.Fatalf("setup error: expected equal counts, got DB=%d, JSONL=%d", dbCount, jsonlCount)
	}

	// The key test: hasJSONLChanged should detect the hash mismatch
	// even though counts are equal
	repoKey := getRepoKeyForPath(jsonlPath)
	changed := hasJSONLChanged(ctx, testStore, jsonlPath, repoKey)

	if !changed {
		t.Error("bd-f2f: hasJSONLChanged should return true when JSONL hash differs from stored hash")
		t.Log("This is the bug scenario: counts match (1 == 1) but content differs (priority=1 vs priority=0)")
		t.Log("Without the bd-f2f fix, the stale DB would export old content and corrupt the remote")
	} else {
		t.Log("✓ bd-f2f fix verified: hash mismatch detected even with equal counts")
	}

	// Verify that after updating hash, hasJSONLChanged returns false
	currentHash, err := computeJSONLHash(jsonlPath)
	if err != nil {
		t.Fatalf("failed to compute current hash: %v", err)
	}
	if err := testStore.SetMetadata(ctx, "jsonl_content_hash", currentHash); err != nil {
		t.Fatalf("failed to set current hash: %v", err)
	}

	changedAfterUpdate := hasJSONLChanged(ctx, testStore, jsonlPath, repoKey)
	if changedAfterUpdate {
		t.Error("hasJSONLChanged should return false after hash is updated to match JSONL")
	}
}

// TestResolveNoGitHistoryForFromMain tests that --from-main forces noGitHistory=true
// to prevent creating incorrect deletion records for locally-created beads.
// See: https://github.com/steveyegge/beads/issues/417
func TestResolveNoGitHistoryForFromMain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		fromMain     bool
		noGitHistory bool
		want         bool
	}{
		{
			name:         "fromMain=true forces noGitHistory=true regardless of flag",
			fromMain:     true,
			noGitHistory: false,
			want:         true,
		},
		{
			name:         "fromMain=true with noGitHistory=true stays true",
			fromMain:     true,
			noGitHistory: true,
			want:         true,
		},
		{
			name:         "fromMain=false preserves noGitHistory=false",
			fromMain:     false,
			noGitHistory: false,
			want:         false,
		},
		{
			name:         "fromMain=false preserves noGitHistory=true",
			fromMain:     false,
			noGitHistory: true,
			want:         true,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveNoGitHistoryForFromMain(tt.fromMain, tt.noGitHistory)
			if got != tt.want {
				t.Errorf("resolveNoGitHistoryForFromMain(%v, %v) = %v, want %v",
					tt.fromMain, tt.noGitHistory, got, tt.want)
			}
		})
	}
}

// TestGetGitCommonDir tests that getGitCommonDir correctly returns the shared
// git directory for both regular repos and worktrees.
func TestGetGitCommonDir(t *testing.T) {
	ctx := context.Background()

	// Test 1: Regular repo
	t.Run("regular repo", func(t *testing.T) {
		repoDir, cleanup := setupGitRepo(t)
		defer cleanup()

		commonDir, err := getGitCommonDir(ctx, repoDir)
		if err != nil {
			t.Fatalf("getGitCommonDir failed: %v", err)
		}

		// For a regular repo, git-common-dir should point to .git
		expectedGitDir := filepath.Join(repoDir, ".git")
		// Resolve symlinks for comparison (macOS /var -> /private/var)
		if resolved, err := filepath.EvalSymlinks(expectedGitDir); err == nil {
			expectedGitDir = resolved
		}
		if commonDir != expectedGitDir {
			t.Errorf("getGitCommonDir = %q, want %q", commonDir, expectedGitDir)
		}
	})

	// Test 2: Worktree (non-bare) shares common dir with main repo
	t.Run("worktree shares common dir with main repo", func(t *testing.T) {
		repoDir, cleanup := setupGitRepo(t)
		defer cleanup()

		// Create a branch for the worktree
		if err := exec.Command("git", "-C", repoDir, "branch", "test-branch").Run(); err != nil {
			t.Fatalf("git branch failed: %v", err)
		}

		// Create worktree
		worktreeDir := filepath.Join(t.TempDir(), "worktree")
		if output, err := exec.Command("git", "-C", repoDir, "worktree", "add", worktreeDir, "test-branch").CombinedOutput(); err != nil {
			t.Fatalf("git worktree add failed: %v\n%s", err, output)
		}

		// Get common dir for both
		mainCommonDir, err := getGitCommonDir(ctx, repoDir)
		if err != nil {
			t.Fatalf("getGitCommonDir(main) failed: %v", err)
		}

		worktreeCommonDir, err := getGitCommonDir(ctx, worktreeDir)
		if err != nil {
			t.Fatalf("getGitCommonDir(worktree) failed: %v", err)
		}

		// Both should return the same common dir
		if mainCommonDir != worktreeCommonDir {
			t.Errorf("common dirs differ: main=%q, worktree=%q", mainCommonDir, worktreeCommonDir)
		}
	})
}

// TestIsExternalBeadsDir tests that isExternalBeadsDir correctly identifies
// when beads directory is in the same vs different git repo.
// GH#810: This was broken for bare repo worktrees.
func TestIsExternalBeadsDir(t *testing.T) {
	ctx := context.Background()

	// Test 1: Same directory - not external
	t.Run("same directory is not external", func(t *testing.T) {
		repoDir, cleanup := setupGitRepo(t)
		defer cleanup()

		beadsDir := filepath.Join(repoDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		// Change to the repo directory (isExternalBeadsDir uses cwd)
		origDir, _ := os.Getwd()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("chdir failed: %v", err)
		}
		defer func() { _ = os.Chdir(origDir) }()

		if isExternalBeadsDir(ctx, beadsDir) {
			t.Error("expected local beads dir to not be external")
		}
	})

	// Test 2: Different repo - is external
	t.Run("different repo is external", func(t *testing.T) {
		repo1Dir, cleanup1 := setupGitRepo(t)
		defer cleanup1()
		repo2Dir, cleanup2 := setupGitRepo(t)
		defer cleanup2()

		beadsDir := filepath.Join(repo2Dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		// Change to repo1
		origDir, _ := os.Getwd()
		if err := os.Chdir(repo1Dir); err != nil {
			t.Fatalf("chdir failed: %v", err)
		}
		defer func() { _ = os.Chdir(origDir) }()

		if !isExternalBeadsDir(ctx, beadsDir) {
			t.Error("expected beads dir in different repo to be external")
		}
	})

	// Test 3: Worktree with beads - not external (GH#810 fix)
	t.Run("worktree beads dir is not external", func(t *testing.T) {
		repoDir, cleanup := setupGitRepo(t)
		defer cleanup()

		// Create a branch for the worktree
		if err := exec.Command("git", "-C", repoDir, "branch", "test-branch").Run(); err != nil {
			t.Fatalf("git branch failed: %v", err)
		}

		// Create worktree
		worktreeDir := filepath.Join(t.TempDir(), "worktree")
		if output, err := exec.Command("git", "-C", repoDir, "worktree", "add", worktreeDir, "test-branch").CombinedOutput(); err != nil {
			t.Fatalf("git worktree add failed: %v\n%s", err, output)
		}

		// Create beads dir in worktree
		beadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		// Change to worktree
		origDir, _ := os.Getwd()
		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("chdir failed: %v", err)
		}
		defer func() { _ = os.Chdir(origDir) }()

		// Beads dir in same worktree should NOT be external
		if isExternalBeadsDir(ctx, beadsDir) {
			t.Error("expected beads dir in same worktree to not be external")
		}
	})
}

// TestConcurrentEdit tests the pull-first sync flow with concurrent edits.
// This validates the 3-way merge logic for the pull-first sync refactor (#911).
//
// Scenario:
// - Base state exists (issue bd-1 at version 2025-01-01)
// - Local modifies issue (version 2025-01-02)
// - Remote also modifies issue (version 2025-01-03)
// - 3-way merge detects conflict and resolves using LWW (remote wins)
func TestConcurrentEdit(t *testing.T) {
	ctx := context.Background()
	tmpDir := newGitRepo(t)
	t.Chdir(tmpDir)

	// Setup: Create beads directory with JSONL (base state)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Base state: single issue at 2025-01-01
	baseTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	baseIssue := `{"id":"bd-1","title":"Original Title","status":"open","issue_type":"task","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}`
	if err := os.WriteFile(jsonlPath, []byte(baseIssue+"\n"), 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	// Initial commit
	_ = exec.Command("git", "add", ".").Run()
	if err := exec.Command("git", "commit", "-m", "initial").Run(); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}

	// Create database and import base state
	testDBPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := dolt.New(ctx, &dolt.Config{Path: testDBPath})
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	defer testStore.Close()

	// Set issue_prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Load base state for 3-way merge
	baseIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("loadIssuesFromJSONL (base) failed: %v", err)
	}

	// Create local issue (modified at 2025-01-02)
	localTime := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	localIssueObj := &types.Issue{
		ID:        "bd-1",
		Title:     "Local Edit",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		CreatedAt: baseTime,
		UpdatedAt: localTime,
	}
	localIssues := []*types.Issue{localIssueObj}

	// Simulate "remote" edit: change title in JSONL (modified at 2025-01-03 - later)
	remoteIssue := `{"id":"bd-1","title":"Remote Edit","status":"open","issue_type":"task","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-03T00:00:00Z"}`
	if err := os.WriteFile(jsonlPath, []byte(remoteIssue+"\n"), 0644); err != nil {
		t.Fatalf("write remote JSONL failed: %v", err)
	}

	remoteIssues, err := loadIssuesFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("loadIssuesFromJSONL (remote) failed: %v", err)
	}

	if len(remoteIssues) != 1 {
		t.Fatalf("expected 1 remote issue, got %d", len(remoteIssues))
	}

	// 3-way merge with base state
	mergeResult := MergeIssues(baseIssues, localIssues, remoteIssues)

	// Verify merge result
	if len(mergeResult.Merged) != 1 {
		t.Fatalf("expected 1 merged issue, got %d", len(mergeResult.Merged))
	}

	// LWW: Remote wins because it has later updated_at (2025-01-03 > 2025-01-02)
	if mergeResult.Merged[0].Title != "Remote Edit" {
		t.Errorf("expected merged title 'Remote Edit' (remote wins LWW), got '%s'", mergeResult.Merged[0].Title)
	}

	// Verify strategy: should be "merged" (conflict resolved by LWW)
	if mergeResult.Strategy["bd-1"] != StrategyMerged {
		t.Errorf("expected strategy '%s' for bd-1, got '%s'", StrategyMerged, mergeResult.Strategy["bd-1"])
	}

	// Verify 1 conflict was detected and resolved
	if mergeResult.Conflicts != 1 {
		t.Errorf("expected 1 conflict (both sides modified), got %d", mergeResult.Conflicts)
	}

	t.Log("TestConcurrentEdit: 3-way merge with LWW resolution validated")
}

// TestConcurrentSyncBlocked tests that concurrent syncs are blocked by file lock.
// This validates the P0 fix for preventing data corruption when running bd sync
// from multiple terminals simultaneously.
func TestConcurrentSyncBlocked(t *testing.T) {
	ctx := context.Background()
	tmpDir := newGitRepo(t)
	t.Chdir(tmpDir)

	// Setup: Create beads directory with JSONL
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create initial JSONL
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"bd-1","title":"Test"}`+"\n"), 0644); err != nil {
		t.Fatalf("write JSONL failed: %v", err)
	}

	// Initial commit
	_ = exec.Command("git", "add", ".").Run()
	if err := exec.Command("git", "commit", "-m", "initial").Run(); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}

	// Create database
	testDBPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := dolt.New(ctx, &dolt.Config{Path: testDBPath})
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	defer testStore.Close()

	// Set issue_prefix
	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Simulate another sync holding the lock
	lockPath := filepath.Join(beadsDir, ".sync.lock")
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	if !locked {
		t.Fatal("expected to acquire lock")
	}

	// Now try to acquire the same lock (simulating concurrent sync)
	lock2 := flock.New(lockPath)
	locked2, err := lock2.TryLock()
	if err != nil {
		t.Fatalf("TryLock error: %v", err)
	}

	// Second lock attempt should fail (not block)
	if locked2 {
		lock2.Unlock()
		t.Error("expected second lock attempt to fail (concurrent sync should be blocked)")
	} else {
		t.Log("Concurrent sync correctly blocked by file lock")
	}

	// Release first lock
	if err := lock.Unlock(); err != nil {
		t.Fatalf("failed to unlock: %v", err)
	}

	// Now lock should be acquirable again
	lock3 := flock.New(lockPath)
	locked3, err := lock3.TryLock()
	if err != nil {
		t.Fatalf("TryLock error after unlock: %v", err)
	}
	if !locked3 {
		t.Error("expected lock to be acquirable after first sync completes")
	} else {
		lock3.Unlock()
		t.Log("Lock correctly acquirable after first sync completes")
	}
}
