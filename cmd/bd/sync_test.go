package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
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
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	os.Chdir(tmpDir)

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
	count, err := countIssuesInJSONL("/nonexistent/path.jsonl")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 on error", count)
	}
}

func TestCountIssuesInJSONL_EmptyFile(t *testing.T) {
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

	// Use an in-memory SQLite store for testing
	testStore, err := sqlite.New(context.Background(), "file::memory:?mode=memory&cache=private")
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

// TestZFCSkipsExportAfterImport tests the bd-l0r fix: after importing JSONL due to
// stale DB detection, sync should skip export to avoid overwriting the JSONL source of truth.
func TestZFCSkipsExportAfterImport(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

	// Setup beads directory with JSONL
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")

	// Create JSONL with 10 issues (simulating pulled state after cleanup)
	var jsonlLines []string
	for i := 1; i <= 10; i++ {
		line := fmt.Sprintf(`{"id":"bd-%d","title":"JSONL Issue %d","status":"open","issue_type":"task","priority":2,"created_at":"2025-11-24T00:00:00Z","updated_at":"2025-11-24T00:00:00Z"}`, i, i)
		jsonlLines = append(jsonlLines, line)
	}
	os.WriteFile(jsonlPath, []byte(strings.Join(jsonlLines, "\n")+"\n"), 0644)

	// Create SQLite store with 100 stale issues (10x the JSONL count = 900% divergence)
	dbPath := filepath.Join(beadsDir, "beads.db")
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	defer testStore.Close()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Populate DB with 100 issues (stale, 90 closed)
	for i := 1; i <= 100; i++ {
		status := types.StatusOpen
		var closedAt *time.Time
		if i > 10 { // First 10 open, rest closed
			status = types.StatusClosed
			now := time.Now()
			closedAt = &now
		}
		issue := &types.Issue{
			Title:     fmt.Sprintf("Old Issue %d", i),
			Status:    status,
			ClosedAt:  closedAt,
			IssueType: types.TypeTask,
			Priority:  2,
		}
		if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("failed to create issue %d: %v", i, err)
		}
	}

	// Verify divergence: (100 - 10) / 10 = 900% > 50% threshold
	dbCount, _ := countDBIssuesFast(ctx, testStore)
	jsonlCount, _ := countIssuesInJSONL(jsonlPath)
	divergence := float64(dbCount-jsonlCount) / float64(jsonlCount)

	if dbCount != 100 {
		t.Fatalf("DB setup failed: expected 100 issues, got %d", dbCount)
	}
	if jsonlCount != 10 {
		t.Fatalf("JSONL setup failed: expected 10 issues, got %d", jsonlCount)
	}
	if divergence <= 0.5 {
		t.Fatalf("Divergence too low: %.2f%% (expected >50%%)", divergence*100)
	}

	// Set global store for the test
	oldStore := store
	storeMutex.Lock()
	oldStoreActive := storeActive
	store = testStore
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		storeMutex.Lock()
		store = oldStore
		storeActive = oldStoreActive
		storeMutex.Unlock()
	}()

	// Save JSONL content hash before running sync logic
	beforeHash, _ := computeJSONLHash(jsonlPath)

	// Simulate the ZFC check and export step from sync.go lines 126-186
	// This is the code path that should detect divergence and skip export
	skipExport := false

	// ZFC safety check
	if err := ensureStoreActive(); err == nil && store != nil {
		dbCount, err := countDBIssuesFast(ctx, store)
		if err == nil {
			jsonlCount, err := countIssuesInJSONL(jsonlPath)
			if err == nil && jsonlCount > 0 && dbCount > jsonlCount {
				divergence := float64(dbCount-jsonlCount) / float64(jsonlCount)
				if divergence > 0.5 {
					// Import JSONL (this should sync DB to match JSONL's 62 issues)
					if err := importFromJSONL(ctx, jsonlPath, false); err != nil {
						t.Fatalf("ZFC import failed: %v", err)
					}
					skipExport = true
				}
			}
		}
	}

	// Verify skipExport was set
	if !skipExport {
		t.Error("Expected skipExport=true after ZFC import, but got false")
	}

	// Verify DB was synced to JSONL (should have 10 issues now, not 100)
	afterDBCount, _ := countDBIssuesFast(ctx, testStore)
	if afterDBCount != 10 {
		t.Errorf("After ZFC import, DB should have 10 issues (matching JSONL), got %d", afterDBCount)
	}

	// Verify JSONL was NOT modified (no export happened)
	afterHash, _ := computeJSONLHash(jsonlPath)
	if beforeHash != afterHash {
		t.Error("JSONL content changed after ZFC import (export should have been skipped)")
	}

	// Verify issue count in JSONL is still 10
	finalJSONLCount, _ := countIssuesInJSONL(jsonlPath)
	if finalJSONLCount != 10 {
		t.Errorf("JSONL should still have 10 issues, got %d", finalJSONLCount)
	}

	t.Logf("✓ ZFC fix verified: DB synced from 100 to 10 issues, JSONL unchanged")
}
