//go:build integration
// +build integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
)

// TestSyncBranchCommitAndPush_NotConfigured tests backward compatibility
// when sync.branch is not configured (should return false, no error)
func TestSyncBranchCommitAndPush_NotConfigured(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create test issue
	issue := &types.Issue{
		Title:       "Test issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Change to temp directory for git operations
	t.Chdir(tmpDir)

	// Test with no sync.branch configured
	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)

	// Should return false (not committed), no error
	if err != nil {
		t.Errorf("Expected no error when sync.branch not configured, got: %v", err)
	}
	if committed {
		t.Error("Expected committed=false when sync.branch not configured")
	}
}

// TestSyncBranchCommitAndPush_Success tests successful sync branch commit
func TestSyncBranchCommitAndPush_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Configure sync.branch
	syncBranch := "beads-sync"
	if err := store.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	// Initial commit on main branch (before creating JSONL)
	t.Chdir(tmpDir)

	initMainBranch(t, tmpDir)

	// Create test issue
	issue := &types.Issue{
		Title:       "Test sync branch issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Test sync branch commit (without push)
	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)

	if err != nil {
		t.Fatalf("syncBranchCommitAndPush failed: %v", err)
	}
	if !committed {
		t.Error("Expected committed=true")
	}

	// Verify worktree was created
	worktreePath := filepath.Join(tmpDir, ".git", "beads-worktrees", syncBranch)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Errorf("Worktree not created at %s", worktreePath)
	}

	// Verify sync branch exists
	cmd := exec.Command("git", "branch", "--list", syncBranch)
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to list branches: %v", err)
	}
	if !strings.Contains(string(output), syncBranch) {
		t.Errorf("Sync branch %s not created", syncBranch)
	}

	// Verify JSONL was synced to worktree
	worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
	if _, err := os.Stat(worktreeJSONL); os.IsNotExist(err) {
		t.Error("JSONL not synced to worktree")
	}

	// Verify commit was made in worktree
	cmd = exec.Command("git", "-C", worktreePath, "log", "--oneline", "-1")
	output, err = cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get log: %v", err)
	}
	if !strings.Contains(string(output), "bd daemon sync") {
		t.Errorf("Expected commit message with 'bd daemon sync', got: %s", string(output))
	}
}

// TestSyncBranchCommitAndPush_EnvOverridesDB verifies that BEADS_SYNC_BRANCH
// takes precedence over the sync.branch database config for daemon commits.
func TestSyncBranchCommitAndPush_EnvOverridesDB(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Configure DB sync.branch to one value
	if err := store.SetConfig(ctx, "sync.branch", "db-branch"); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	// Set BEADS_SYNC_BRANCH to a different value and ensure it takes precedence.
	t.Setenv(syncbranch.EnvVar, "env-branch")

	// Initial commit on main branch
	t.Chdir(tmpDir)

	initMainBranch(t, tmpDir)

	// Create test issue and export JSONL
	issue := &types.Issue{
		Title:       "Env override issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	log, _ := newTestSyncBranchLogger()
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("syncBranchCommitAndPush failed: %v", err)
	}
	if !committed {
		t.Fatal("Expected committed=true with env override")
	}

	// Verify that the worktree and branch are created using the env branch.
	worktreePath := filepath.Join(tmpDir, ".git", "beads-worktrees", "env-branch")
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatalf("Env sync branch worktree not created at %s", worktreePath)
	}

	cmd := exec.Command("git", "branch", "--list", "env-branch")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to list branches: %v", err)
	}
	if !strings.Contains(string(output), "env-branch") {
		t.Errorf("Env sync branch not created, branches: %s", string(output))
	}
}

// TestSyncBranchCommitAndPush_NoChanges tests behavior when no changes to commit
func TestSyncBranchCommitAndPush_NoChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	syncBranch := "beads-sync"
	if err := store.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	issue := &types.Issue{
		Title:       "Test issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	t.Chdir(tmpDir)

	log, logMsgs := newTestSyncBranchLogger()

	// First commit should succeed
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("First commit failed: %v", err)
	}
	if !committed {
		t.Error("Expected first commit to succeed")
	}

	// Second commit with no changes should return false
	committed, err = syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("Second commit failed: %v", err)
	}
	if committed {
		t.Error("Expected committed=false when no changes")
	}

	// Verify log message
	if !strings.Contains(*logMsgs, "No changes to commit") {
		t.Error("Expected 'No changes to commit' log message")
	}
}

// TestSyncBranchCommitAndPush_WorktreeHealthCheck tests worktree repair logic
func TestSyncBranchCommitAndPush_WorktreeHealthCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	syncBranch := "beads-sync"
	if err := store.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	issue := &types.Issue{
		Title:       "Test issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	t.Chdir(tmpDir)

	log, logMsgs := newTestSyncBranchLogger()

	// First commit to create worktree
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("First commit failed: %v", err)
	}
	if !committed {
		t.Error("Expected first commit to succeed")
	}

	// Corrupt the worktree by deleting .git file
	worktreePath := filepath.Join(tmpDir, ".git", "beads-worktrees", syncBranch)
	worktreeGitFile := filepath.Join(worktreePath, ".git")
	if err := os.Remove(worktreeGitFile); err != nil {
		t.Fatalf("Failed to corrupt worktree: %v", err)
	}

	// Update issue to create new changes
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"priority": 2,
	}, "test"); err != nil {
		t.Fatalf("Failed to update issue: %v", err)
	}

	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	*logMsgs = "" // Reset log

	// Should detect corruption and repair (CreateBeadsWorktree handles this silently)
	committed, err = syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("Commit after corruption failed: %v", err)
	}
	if !committed {
		t.Error("Expected commit to succeed after repair")
	}

	// Verify worktree is functional again - .git file should be restored
	if _, err := os.Stat(worktreeGitFile); os.IsNotExist(err) {
		t.Error("Worktree .git file not restored")
	}
}

// TestSyncBranchPull_NotConfigured tests pull with no sync.branch configured
func TestSyncBranchPull_NotConfigured(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	t.Chdir(tmpDir)

	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	pulled, err := syncBranchPull(ctx, store, log)

	// Should return false (not pulled), no error
	if err != nil {
		t.Errorf("Expected no error when sync.branch not configured, got: %v", err)
	}
	if pulled {
		t.Error("Expected pulled=false when sync.branch not configured")
	}
}

// TestSyncBranchPull_Success tests successful pull from sync branch
func TestSyncBranchPull_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create remote repository
	tmpDir := t.TempDir()
	remoteDir := filepath.Join(tmpDir, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatalf("Failed to create remote dir: %v", err)
	}
	runGitCmd(t, remoteDir, "init", "--bare")

	// Create clone1 (will push changes)
	clone1Dir := filepath.Join(tmpDir, "clone1")
	runGitCmd(t, tmpDir, "clone", remoteDir, clone1Dir)
	configureGit(t, clone1Dir)

	clone1BeadsDir := filepath.Join(clone1Dir, ".beads")
	if err := os.MkdirAll(clone1BeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	clone1DBPath := filepath.Join(clone1BeadsDir, "test.db")
	store1, err := sqlite.New(context.Background(), clone1DBPath)
	if err != nil {
		t.Fatalf("Failed to create store1: %v", err)
	}
	defer store1.Close()

	ctx := context.Background()
	if err := store1.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	syncBranch := "beads-sync"
	if err := store1.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	// Create issue in clone1
	issue := &types.Issue{
		Title:       "Test sync pull issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store1.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	clone1JSONLPath := filepath.Join(clone1BeadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store1, clone1JSONLPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Commit to main branch first
	initMainBranch(t, clone1Dir)
	runGitCmd(t, clone1Dir, "push", "origin", "master")

	// Change to clone1 directory for sync branch operations
	t.Chdir(clone1Dir)

	// Push to sync branch using syncBranchCommitAndPush
	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	committed, err := syncBranchCommitAndPush(ctx, store1, true, log)
	if err != nil {
		t.Fatalf("syncBranchCommitAndPush failed: %v", err)
	}
	if !committed {
		t.Error("Expected commit to succeed")
	}

	// Create clone2 (will pull changes)
	clone2Dir := filepath.Join(tmpDir, "clone2")
	runGitCmd(t, tmpDir, "clone", remoteDir, clone2Dir)
	configureGit(t, clone2Dir)

	clone2BeadsDir := filepath.Join(clone2Dir, ".beads")
	clone2DBPath := filepath.Join(clone2BeadsDir, "test.db")
	store2, err := sqlite.New(context.Background(), clone2DBPath)
	if err != nil {
		t.Fatalf("Failed to create store2: %v", err)
	}
	defer store2.Close()

	if err := store2.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	if err := store2.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	// Change to clone2 directory
	t.Chdir(clone2Dir)

	// Pull from sync branch
	log2, logMsgs2 := newTestSyncBranchLogger()
	pulled, err := syncBranchPull(ctx, store2, log2)
	if err != nil {
		t.Fatalf("syncBranchPull failed: %v", err)
	}
	if !pulled {
		t.Error("Expected pulled=true")
	}

	// Verify JSONL was copied to main repo
	clone2JSONLPath := filepath.Join(clone2BeadsDir, "issues.jsonl")
	if _, err := os.Stat(clone2JSONLPath); os.IsNotExist(err) {
		t.Error("JSONL not copied to main repo after pull")
	}

	// On Windows, file I/O may need more time to settle
	// Increase delay significantly for reliable CI tests
	if runtime.GOOS == "windows" {
		time.Sleep(300 * time.Millisecond)
	}

	// Verify JSONL content matches
	clone1Data, err := os.ReadFile(clone1JSONLPath)
	if err != nil {
		t.Fatalf("Failed to read clone1 JSONL: %v", err)
	}

	clone2Data, err := os.ReadFile(clone2JSONLPath)
	if err != nil {
		t.Fatalf("Failed to read clone2 JSONL: %v", err)
	}

	if string(clone1Data) != string(clone2Data) {
		t.Error("JSONL content mismatch after pull")
	}

	// Verify pull message in log
	if !strings.Contains(*logMsgs2, "Pulled sync branch") {
		t.Error("Expected 'Pulled sync branch' log message")
	}
}

// TestSyncBranchIntegration_EndToEnd tests full sync workflow
func TestSyncBranchIntegration_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup remote and two clones
	tmpDir := t.TempDir()
	remoteDir := filepath.Join(tmpDir, "remote")
	os.MkdirAll(remoteDir, 0755)
	runGitCmd(t, remoteDir, "init", "--bare")

	// Clone1: Agent A
	clone1Dir := filepath.Join(tmpDir, "clone1")
	runGitCmd(t, tmpDir, "clone", remoteDir, clone1Dir)
	configureGit(t, clone1Dir)

	clone1BeadsDir := filepath.Join(clone1Dir, ".beads")
	os.MkdirAll(clone1BeadsDir, 0755)
	clone1DBPath := filepath.Join(clone1BeadsDir, "test.db")
	store1, _ := sqlite.New(context.Background(), clone1DBPath)
	defer store1.Close()

	ctx := context.Background()
	store1.SetConfig(ctx, "issue_prefix", "test")

	syncBranch := "beads-sync"
	store1.SetConfig(ctx, "sync.branch", syncBranch)

	// Agent A creates issue
	issue := &types.Issue{
		Title:       "E2E test issue",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	store1.CreateIssue(ctx, issue, "agent-a")
	issueID := issue.ID

	clone1JSONLPath := filepath.Join(clone1BeadsDir, "issues.jsonl")
	exportToJSONLWithStore(ctx, store1, clone1JSONLPath)

	// Initial commit to main
	initMainBranch(t, clone1Dir)
	runGitCmd(t, clone1Dir, "push", "origin", "master")

	// Change to clone1 directory
	t.Chdir(clone1Dir)

	// Agent A commits to sync branch
	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	committed, err := syncBranchCommitAndPush(ctx, store1, true, log)
	if err != nil {
		t.Fatalf("syncBranchCommitAndPush failed: %v", err)
	}
	if !committed {
		t.Error("Expected commit to succeed")
	}

	// Clone2: Agent B
	clone2Dir := filepath.Join(tmpDir, "clone2")
	runGitCmd(t, tmpDir, "clone", remoteDir, clone2Dir)
	configureGit(t, clone2Dir)

	clone2BeadsDir := filepath.Join(clone2Dir, ".beads")
	clone2DBPath := filepath.Join(clone2BeadsDir, "test.db")
	store2, _ := sqlite.New(context.Background(), clone2DBPath)
	defer store2.Close()

	store2.SetConfig(ctx, "issue_prefix", "test")
	store2.SetConfig(ctx, "sync.branch", syncBranch)

	// Change to clone2 directory
	t.Chdir(clone2Dir)

	// Agent B pulls from sync branch
	log2, logMsgs2 := newTestSyncBranchLogger()
	_ = logMsgs2 // unused in this test
	pulled, err := syncBranchPull(ctx, store2, log2)
	if err != nil {
		t.Fatalf("syncBranchPull failed: %v", err)
	}
	if !pulled {
		t.Error("Expected pull to succeed")
	}

	// Import JSONL to database
	clone2JSONLPath := filepath.Join(clone2BeadsDir, "issues.jsonl")
	if err := importToJSONLWithStore(ctx, store2, clone2JSONLPath); err != nil {
		t.Fatalf("Failed to import: %v", err)
	}

	// Verify issue exists in clone2
	clone2Issue, err := store2.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("Failed to get issue in clone2: %v", err)
	}
	if clone2Issue.Title != issue.Title {
		t.Errorf("Issue title mismatch: expected %s, got %s", issue.Title, clone2Issue.Title)
	}

	// Agent B closes the issue
	store2.CloseIssue(ctx, issueID, "Done by Agent B", "agent-b")
	exportToJSONLWithStore(ctx, store2, clone2JSONLPath)

	// Agent B commits to sync branch
	committed, err = syncBranchCommitAndPush(ctx, store2, true, log2)
	if err != nil {
		t.Fatalf("syncBranchCommitAndPush failed for clone2: %v", err)
	}
	if !committed {
		t.Error("Expected commit to succeed for clone2")
	}

	// Agent A pulls the update
	t.Chdir(clone1Dir)
	pulled, err = syncBranchPull(ctx, store1, log)
	if err != nil {
		t.Fatalf("syncBranchPull failed for clone1: %v", err)
	}
	if !pulled {
		t.Error("Expected pull to succeed for clone1")
	}

	// Import to see the closed status
	importToJSONLWithStore(ctx, store1, clone1JSONLPath)

	// Verify Agent A sees the closed issue
	updatedIssue, err := store1.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("Failed to get issue in clone1: %v", err)
	}
	if updatedIssue.Status != types.StatusClosed {
		t.Errorf("Issue not closed in clone1: status=%s", updatedIssue.Status)
	}
}

// Helper types for testing

func newTestSyncBranchLogger() (daemonLogger, *string) {
	messages := ""
	logger := daemonLogger{
		logFunc: func(format string, args ...interface{}) {
			messages += "\n" + format
		},
	}
	return logger, &messages
}

// TestSyncBranchConfigChange tests changing sync.branch after worktree exists
func TestSyncBranchConfigChange(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set initial sync.branch
	syncBranch1 := "beads-sync-v1"
	if err := store.SetConfig(ctx, "sync.branch", syncBranch1); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	issue := &types.Issue{
		Title:     "Test config change",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	t.Chdir(tmpDir)

	log, _ := newTestSyncBranchLogger()

	// First commit to v1 branch
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("First commit failed: %v", err)
	}
	if !committed {
		t.Error("Expected first commit to succeed")
	}

	// Verify v1 worktree exists
	worktree1Path := filepath.Join(tmpDir, ".git", "beads-worktrees", syncBranch1)
	if _, err := os.Stat(worktree1Path); os.IsNotExist(err) {
		t.Errorf("Worktree v1 not created at %s", worktree1Path)
	}

	// Change sync.branch to v2
	syncBranch2 := "beads-sync-v2"
	if err := store.SetConfig(ctx, "sync.branch", syncBranch2); err != nil {
		t.Fatalf("Failed to change sync.branch: %v", err)
	}

	// Update issue to create new changes
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"priority": 2,
	}, "test"); err != nil {
		t.Fatalf("Failed to update issue: %v", err)
	}

	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Commit to v2 branch (should create new worktree)
	committed, err = syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("Second commit failed: %v", err)
	}
	if !committed {
		t.Error("Expected second commit to succeed")
	}

	// Verify v2 worktree exists
	worktree2Path := filepath.Join(tmpDir, ".git", "beads-worktrees", syncBranch2)
	if _, err := os.Stat(worktree2Path); os.IsNotExist(err) {
		t.Errorf("Worktree v2 not created at %s", worktree2Path)
	}

	// Verify both branches exist
	cmd := exec.Command("git", "branch", "--list")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to list branches: %v", err)
	}
	branches := string(output)
	if !strings.Contains(branches, syncBranch1) {
		t.Errorf("Branch %s not found", syncBranch1)
	}
	if !strings.Contains(branches, syncBranch2) {
		t.Errorf("Branch %s not found", syncBranch2)
	}

	// Verify both worktrees exist and are valid
	if _, err := os.Stat(worktree1Path); err != nil {
		t.Error("Old worktree v1 should still exist")
	}
	if _, err := os.Stat(worktree2Path); err != nil {
		t.Error("New worktree v2 should exist")
	}
}

// TestSyncBranchMultipleConcurrentClones tests three clones working simultaneously
func TestSyncBranchMultipleConcurrentClones(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup remote and three clones
	tmpDir := t.TempDir()
	remoteDir := filepath.Join(tmpDir, "remote")
	os.MkdirAll(remoteDir, 0755)
	runGitCmd(t, remoteDir, "init", "--bare")

	syncBranch := "beads-sync"

	// Helper to setup a clone
	setupClone := func(name string) (string, *sqlite.SQLiteStorage) {
		cloneDir := filepath.Join(tmpDir, name)
		runGitCmd(t, tmpDir, "clone", remoteDir, cloneDir)
		configureGit(t, cloneDir)

		beadsDir := filepath.Join(cloneDir, ".beads")
		os.MkdirAll(beadsDir, 0755)
		dbPath := filepath.Join(beadsDir, "test.db")
		store, _ := sqlite.New(context.Background(), dbPath)

		ctx := context.Background()
		store.SetConfig(ctx, "issue_prefix", "test")
		store.SetConfig(ctx, "sync.branch", syncBranch)

		return cloneDir, store
	}

	// Setup three clones
	clone1Dir, store1 := setupClone("clone1")
	defer store1.Close()
	clone2Dir, store2 := setupClone("clone2")
	defer store2.Close()
	clone3Dir, store3 := setupClone("clone3")
	defer store3.Close()

	ctx := context.Background()

	// Initial commit on main
	initMainBranch(t, clone1Dir)
	runGitCmd(t, clone1Dir, "push", "origin", "master")

	// Clone1: Create and push issue A
	t.Chdir(clone1Dir)
	issueA := &types.Issue{
		Title:     "Issue A from clone1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store1.CreateIssue(ctx, issueA, "agent1")
	jsonlPath1 := filepath.Join(clone1Dir, ".beads", "issues.jsonl")
	exportToJSONLWithStore(ctx, store1, jsonlPath1)

	log1, _ := newTestSyncBranchLogger()
	committed, err := syncBranchCommitAndPush(ctx, store1, true, log1)
	if err != nil || !committed {
		t.Fatalf("Clone1 commit failed: err=%v, committed=%v", err, committed)
	}

	// Clone2: Fetch, pull, create issue B, push
	t.Chdir(clone2Dir)
	runGitCmd(t, clone2Dir, "fetch", "origin")
	log2, _ := newTestSyncBranchLogger()
	syncBranchPull(ctx, store2, log2)
	jsonlPath2 := filepath.Join(clone2Dir, ".beads", "issues.jsonl")
	importToJSONLWithStore(ctx, store2, jsonlPath2)

	issueB := &types.Issue{
		Title:     "Issue B from clone2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store2.CreateIssue(ctx, issueB, "agent2")
	exportToJSONLWithStore(ctx, store2, jsonlPath2)
	committed, err = syncBranchCommitAndPush(ctx, store2, true, log2)
	if err != nil || !committed {
		t.Fatalf("Clone2 commit failed: err=%v, committed=%v", err, committed)
	}

	// Clone3: Fetch, pull, create issue C, push
	t.Chdir(clone3Dir)
	runGitCmd(t, clone3Dir, "fetch", "origin")
	log3, _ := newTestSyncBranchLogger()
	syncBranchPull(ctx, store3, log3)
	jsonlPath3 := filepath.Join(clone3Dir, ".beads", "issues.jsonl")
	importToJSONLWithStore(ctx, store3, jsonlPath3)

	issueC := &types.Issue{
		Title:     "Issue C from clone3",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store3.CreateIssue(ctx, issueC, "agent3")
	exportToJSONLWithStore(ctx, store3, jsonlPath3)
	committed, err = syncBranchCommitAndPush(ctx, store3, true, log3)
	if err != nil || !committed {
		t.Fatalf("Clone3 commit failed: err=%v, committed=%v", err, committed)
	}

	// All clones pull final state
	t.Chdir(clone1Dir)
	syncBranchPull(ctx, store1, log1)
	importToJSONLWithStore(ctx, store1, jsonlPath1)

	t.Chdir(clone2Dir)
	syncBranchPull(ctx, store2, log2)
	importToJSONLWithStore(ctx, store2, jsonlPath2)

	// Verify all three issues exist in all clones
	verifyIssueCount := func(store *sqlite.SQLiteStorage, expected int, cloneName string) {
		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Errorf("%s: Failed to search issues: %v", cloneName, err)
		}
		if len(issues) != expected {
			t.Errorf("%s: Expected %d issues, got %d", cloneName, expected, len(issues))
		}
	}

	verifyIssueCount(store1, 3, "clone1")
	verifyIssueCount(store2, 3, "clone2")
	verifyIssueCount(store3, 3, "clone3")

	// Verify specific issues exist
	verifyIssueExists := func(store *sqlite.SQLiteStorage, id, cloneName string) {
		_, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("%s: Issue %s not found: %v", cloneName, id, err)
		}
	}

	verifyIssueExists(store1, issueA.ID, "clone1")
	verifyIssueExists(store1, issueB.ID, "clone1")
	verifyIssueExists(store1, issueC.ID, "clone1")
}

// TestSyncBranchPerformance tests that sync branch operations have acceptable overhead
func TestSyncBranchPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	store.SetConfig(ctx, "issue_prefix", "test")
	store.SetConfig(ctx, "sync.branch", "beads-sync")

	// Create initial issue
	issue := &types.Issue{
		Title:     "Performance test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store.CreateIssue(ctx, issue, "test")

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	exportToJSONLWithStore(ctx, store, jsonlPath)

	t.Chdir(tmpDir)

	log, _ := newTestSyncBranchLogger()

	// First commit (creates worktree - expected to be slower)
	start := time.Now()
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)
	firstDuration := time.Since(start)
	if err != nil || !committed {
		t.Fatalf("First commit failed: err=%v, committed=%v", err, committed)
	}

	t.Logf("First commit (with worktree creation): %v", firstDuration)

	// Subsequent commits (should be fast)
	const iterations = 5
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		// Make a small change
		store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
			"priority": (i % 4) + 1,
		}, "test")
		exportToJSONLWithStore(ctx, store, jsonlPath)

		start = time.Now()
		committed, err = syncBranchCommitAndPush(ctx, store, false, log)
		duration := time.Since(start)
		totalDuration += duration

		if err != nil || !committed {
			t.Fatalf("Commit %d failed: err=%v, committed=%v", i+1, err, committed)
		}

		t.Logf("Commit %d: %v", i+1, duration)
	}

	avgDuration := totalDuration / iterations
	// Windows git operations are significantly slower - use platform-specific thresholds
	maxAllowed := 150 * time.Millisecond
	if runtime.GOOS == "windows" {
		maxAllowed = 500 * time.Millisecond
	}

	t.Logf("Average commit time: %v (max allowed: %v)", avgDuration, maxAllowed)

	if avgDuration > maxAllowed {
		t.Errorf("Average commit overhead %v exceeds maximum allowed %v", avgDuration, maxAllowed)
	}
}

// TestSyncBranchNetworkFailure tests graceful handling of network errors
func TestSyncBranchNetworkFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)

	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	store.SetConfig(ctx, "issue_prefix", "test")
	store.SetConfig(ctx, "sync.branch", "beads-sync")

	// Create issue
	issue := &types.Issue{
		Title:     "Test network failure",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store.CreateIssue(ctx, issue, "test")

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	exportToJSONLWithStore(ctx, store, jsonlPath)

	t.Chdir(tmpDir)

	log, logMsgs := newTestSyncBranchLogger()

	// Commit locally (without push to simulate offline mode)
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)
	if err != nil {
		t.Fatalf("Local commit failed: %v", err)
	}
	if !committed {
		t.Error("Expected commit to succeed locally")
	}

	// Now try to push to non-existent remote (simulates network failure)
	// Set up a bogus remote
	runGitCmd(t, tmpDir, "remote", "add", "origin", "https://invalid-remote.example.com/repo.git")

	// Update issue
	store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"priority": 2,
	}, "test")
	exportToJSONLWithStore(ctx, store, jsonlPath)

	// Try commit with push - should handle network error gracefully
	committed, err = syncBranchCommitAndPush(ctx, store, true, log)

	// The commit should succeed locally even if push fails
	// (Current implementation may vary - this documents expected behavior)
	pushFailed := false
	if err != nil {
		// Network error is acceptable - verify it's a git/network error
		if !strings.Contains(err.Error(), "git") && !strings.Contains(err.Error(), "push") {
			t.Errorf("Expected git/push error, got: %v", err)
		}
		t.Logf("Network error (expected): %v", err)
		pushFailed = true
	}

	// Verify local commit still succeeded
	worktreePath := filepath.Join(tmpDir, ".git", "beads-worktrees", "beads-sync")
	cmd := exec.Command("git", "-C", worktreePath, "log", "--oneline")
	output, cmdErr := cmd.Output()
	if cmdErr != nil {
		t.Fatalf("Failed to get log: %v", cmdErr)
	}

	// Should have at least 2 commits (initial + update)
	commits := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(commits) < 2 {
		t.Errorf("Expected at least 2 commits, got %d", len(commits))
	}

	// Verify log contains appropriate messages
	// If push failed, we might not have the success message
	if !pushFailed {
		if !strings.Contains(*logMsgs, "Committed") || !strings.Contains(*logMsgs, "beads-sync") {
			t.Error("Expected commit success message in log")
		}
	}
}

// initMainBranch creates an initial commit on main branch
// The JSONL file should not exist yet when this is called
func initMainBranch(t *testing.T, dir string) {
	t.Helper()
	// Create a simple README to have something to commit
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# Test Repository\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}
	runGitCmd(t, dir, "add", "README.md")
	runGitCmd(t, dir, "commit", "-m", "Initial commit")
}
