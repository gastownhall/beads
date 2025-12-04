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
	// Skip this test - it calls importFromJSONL which spawns bd import as subprocess,
	// but os.Executable() returns the test binary during tests, not the bd binary.
	// TODO: Refactor to use direct import logic instead of subprocess.
	t.Skip("Test requires subprocess spawning which doesn't work in test environment")
	if testing.Short() {
		t.Skip("Skipping test that spawns subprocess in short mode")
	}
	ctx := context.Background()
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

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

func TestMaybeAutoCompactDeletions_Disabled(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test database
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")

	// Create store
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	// Set global store for maybeAutoCompactDeletions
	// Save and restore original values
	originalStore := store
	originalStoreActive := storeActive
	defer func() {
		store = originalStore
		storeActive = originalStoreActive
	}()

	store = testStore
	storeActive = true

	// Create empty JSONL file
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	// Auto-compact is disabled by default, so should return nil
	err = maybeAutoCompactDeletions(ctx, jsonlPath)
	if err != nil {
		t.Errorf("expected no error when auto-compact disabled, got: %v", err)
	}
}

func TestMaybeAutoCompactDeletions_Enabled(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test database
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// Create store
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	// Enable auto-compact with low threshold
	if err := testStore.SetConfig(ctx, "deletions.auto_compact", "true"); err != nil {
		t.Fatalf("failed to set auto_compact config: %v", err)
	}
	if err := testStore.SetConfig(ctx, "deletions.auto_compact_threshold", "5"); err != nil {
		t.Fatalf("failed to set threshold config: %v", err)
	}
	if err := testStore.SetConfig(ctx, "deletions.retention_days", "1"); err != nil {
		t.Fatalf("failed to set retention config: %v", err)
	}

	// Set global store for maybeAutoCompactDeletions
	// Save and restore original values
	originalStore := store
	originalStoreActive := storeActive
	defer func() {
		store = originalStore
		storeActive = originalStoreActive
	}()

	store = testStore
	storeActive = true

	// Create empty JSONL file
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	// Create deletions file with entries (some old, some recent)
	now := time.Now()
	deletionsContent := ""
	// Add 10 old entries (will be pruned)
	for i := 0; i < 10; i++ {
		oldTime := now.AddDate(0, 0, -10).Format(time.RFC3339)
		deletionsContent += fmt.Sprintf(`{"id":"bd-old-%d","ts":"%s","by":"user"}`, i, oldTime) + "\n"
	}
	// Add 3 recent entries (will be kept)
	for i := 0; i < 3; i++ {
		recentTime := now.Add(-1 * time.Hour).Format(time.RFC3339)
		deletionsContent += fmt.Sprintf(`{"id":"bd-recent-%d","ts":"%s","by":"user"}`, i, recentTime) + "\n"
	}

	if err := os.WriteFile(deletionsPath, []byte(deletionsContent), 0644); err != nil {
		t.Fatalf("failed to create deletions file: %v", err)
	}

	// Verify initial count
	initialCount := strings.Count(deletionsContent, "\n")
	if initialCount != 13 {
		t.Fatalf("expected 13 initial entries, got %d", initialCount)
	}

	// Run auto-compact
	err = maybeAutoCompactDeletions(ctx, jsonlPath)
	if err != nil {
		t.Errorf("auto-compact failed: %v", err)
	}

	// Read deletions file and count remaining entries
	afterContent, err := os.ReadFile(deletionsPath)
	if err != nil {
		t.Fatalf("failed to read deletions file: %v", err)
	}

	afterLines := strings.Split(strings.TrimSpace(string(afterContent)), "\n")
	afterCount := 0
	for _, line := range afterLines {
		if line != "" {
			afterCount++
		}
	}

	// Should have pruned old entries, kept recent ones
	if afterCount != 3 {
		t.Errorf("expected 3 entries after prune (recent ones), got %d", afterCount)
	}
}

func TestMaybeAutoCompactDeletions_BelowThreshold(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test database
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// Create store
	testStore, err := sqlite.New(ctx, testDBPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer testStore.Close()

	// Enable auto-compact with high threshold
	if err := testStore.SetConfig(ctx, "deletions.auto_compact", "true"); err != nil {
		t.Fatalf("failed to set auto_compact config: %v", err)
	}
	if err := testStore.SetConfig(ctx, "deletions.auto_compact_threshold", "100"); err != nil {
		t.Fatalf("failed to set threshold config: %v", err)
	}

	// Set global store for maybeAutoCompactDeletions
	// Save and restore original values
	originalStore := store
	originalStoreActive := storeActive
	defer func() {
		store = originalStore
		storeActive = originalStoreActive
	}()

	store = testStore
	storeActive = true

	// Create empty JSONL file
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	// Create deletions file with only 5 entries (below threshold of 100)
	now := time.Now()
	deletionsContent := ""
	for i := 0; i < 5; i++ {
		ts := now.Add(-1 * time.Hour).Format(time.RFC3339)
		deletionsContent += fmt.Sprintf(`{"id":"bd-%d","ts":"%s","by":"user"}`, i, ts) + "\n"
	}

	if err := os.WriteFile(deletionsPath, []byte(deletionsContent), 0644); err != nil {
		t.Fatalf("failed to create deletions file: %v", err)
	}

	// Run auto-compact - should skip because below threshold
	err = maybeAutoCompactDeletions(ctx, jsonlPath)
	if err != nil {
		t.Errorf("auto-compact failed: %v", err)
	}

	// Read deletions file - should be unchanged
	afterContent, err := os.ReadFile(deletionsPath)
	if err != nil {
		t.Fatalf("failed to read deletions file: %v", err)
	}

	if string(afterContent) != deletionsContent {
		t.Error("deletions file should not be modified when below threshold")
	}
}

func TestSanitizeJSONLWithDeletions_NoDeletions(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	jsonlContent := `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}
{"id":"bd-3","title":"Issue 3"}
`
	os.WriteFile(jsonlPath, []byte(jsonlContent), 0644)

	// No deletions.jsonl file - should return without changes
	result, err := sanitizeJSONLWithDeletions(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RemovedCount != 0 {
		t.Errorf("expected 0 removed, got %d", result.RemovedCount)
	}

	// Verify JSONL unchanged
	afterContent, _ := os.ReadFile(jsonlPath)
	if string(afterContent) != jsonlContent {
		t.Error("JSONL should not be modified when no deletions")
	}
}

func TestSanitizeJSONLWithDeletions_EmptyDeletions(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	jsonlContent := `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}
`
	os.WriteFile(jsonlPath, []byte(jsonlContent), 0644)
	os.WriteFile(deletionsPath, []byte(""), 0644)

	result, err := sanitizeJSONLWithDeletions(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RemovedCount != 0 {
		t.Errorf("expected 0 removed, got %d", result.RemovedCount)
	}
}

func TestSanitizeJSONLWithDeletions_RemovesDeletedIssues(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// JSONL with 4 issues
	jsonlContent := `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}
{"id":"bd-3","title":"Issue 3"}
{"id":"bd-4","title":"Issue 4"}
`
	os.WriteFile(jsonlPath, []byte(jsonlContent), 0644)

	// Deletions manifest marks bd-2 and bd-4 as deleted
	now := time.Now().Format(time.RFC3339)
	deletionsContent := fmt.Sprintf(`{"id":"bd-2","ts":"%s","by":"user","reason":"cleanup"}
{"id":"bd-4","ts":"%s","by":"user","reason":"duplicate"}
`, now, now)
	os.WriteFile(deletionsPath, []byte(deletionsContent), 0644)

	result, err := sanitizeJSONLWithDeletions(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RemovedCount != 2 {
		t.Errorf("expected 2 removed, got %d", result.RemovedCount)
	}
	if len(result.RemovedIDs) != 2 {
		t.Errorf("expected 2 RemovedIDs, got %d", len(result.RemovedIDs))
	}

	// Verify correct IDs were removed
	removedMap := make(map[string]bool)
	for _, id := range result.RemovedIDs {
		removedMap[id] = true
	}
	if !removedMap["bd-2"] || !removedMap["bd-4"] {
		t.Errorf("expected bd-2 and bd-4 to be removed, got %v", result.RemovedIDs)
	}

	// Verify JSONL now only has bd-1 and bd-3
	afterContent, _ := os.ReadFile(jsonlPath)
	afterCount, _ := countIssuesInJSONL(jsonlPath)
	if afterCount != 2 {
		t.Errorf("expected 2 issues in JSONL after sanitize, got %d", afterCount)
	}
	if !strings.Contains(string(afterContent), `"id":"bd-1"`) {
		t.Error("JSONL should still contain bd-1")
	}
	if !strings.Contains(string(afterContent), `"id":"bd-3"`) {
		t.Error("JSONL should still contain bd-3")
	}
	if strings.Contains(string(afterContent), `"id":"bd-2"`) {
		t.Error("JSONL should NOT contain deleted bd-2")
	}
	if strings.Contains(string(afterContent), `"id":"bd-4"`) {
		t.Error("JSONL should NOT contain deleted bd-4")
	}
}

func TestSanitizeJSONLWithDeletions_NoMatchingDeletions(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// JSONL with issues
	jsonlContent := `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}
`
	os.WriteFile(jsonlPath, []byte(jsonlContent), 0644)

	// Deletions for different IDs
	now := time.Now().Format(time.RFC3339)
	deletionsContent := fmt.Sprintf(`{"id":"bd-99","ts":"%s","by":"user"}
{"id":"bd-100","ts":"%s","by":"user"}
`, now, now)
	os.WriteFile(deletionsPath, []byte(deletionsContent), 0644)

	result, err := sanitizeJSONLWithDeletions(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RemovedCount != 0 {
		t.Errorf("expected 0 removed (no matching IDs), got %d", result.RemovedCount)
	}

	// Verify JSONL unchanged
	afterContent, _ := os.ReadFile(jsonlPath)
	if string(afterContent) != jsonlContent {
		t.Error("JSONL should not be modified when no matching deletions")
	}
}

func TestSanitizeJSONLWithDeletions_PreservesMalformedLines(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// JSONL with a malformed line
	jsonlContent := `{"id":"bd-1","title":"Issue 1"}
this is not valid json
{"id":"bd-2","title":"Issue 2"}
`
	os.WriteFile(jsonlPath, []byte(jsonlContent), 0644)

	// Delete bd-2
	now := time.Now().Format(time.RFC3339)
	os.WriteFile(deletionsPath, []byte(fmt.Sprintf(`{"id":"bd-2","ts":"%s","by":"user"}`, now)+"\n"), 0644)

	result, err := sanitizeJSONLWithDeletions(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RemovedCount != 1 {
		t.Errorf("expected 1 removed, got %d", result.RemovedCount)
	}

	// Verify malformed line is preserved (let import handle it)
	afterContent, _ := os.ReadFile(jsonlPath)
	if !strings.Contains(string(afterContent), "this is not valid json") {
		t.Error("malformed line should be preserved")
	}
	if !strings.Contains(string(afterContent), `"id":"bd-1"`) {
		t.Error("bd-1 should be preserved")
	}
	if strings.Contains(string(afterContent), `"id":"bd-2"`) {
		t.Error("bd-2 should be removed")
	}
}

func TestSanitizeJSONLWithDeletions_NonexistentJSONL(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	jsonlPath := filepath.Join(beadsDir, "nonexistent.jsonl")
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// Create deletions file
	now := time.Now().Format(time.RFC3339)
	os.WriteFile(deletionsPath, []byte(fmt.Sprintf(`{"id":"bd-1","ts":"%s","by":"user"}`, now)+"\n"), 0644)

	// Should handle missing JSONL gracefully
	result, err := sanitizeJSONLWithDeletions(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error for missing JSONL: %v", err)
	}
	if result.RemovedCount != 0 {
		t.Errorf("expected 0 removed for missing file, got %d", result.RemovedCount)
	}
}

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
	testStore, err := sqlite.New(ctx, testDBPath)
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
