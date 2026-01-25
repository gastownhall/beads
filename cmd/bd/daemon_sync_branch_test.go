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

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
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
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
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
		Title:     "Test sync branch issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
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
		Title:     "Env override issue",
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
		Title:     "Test issue",
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
		Title:     "Test issue",
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
	runGitCmd(t, remoteDir, "init", "--bare", "-b", "master")

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
		Title:     "Test sync pull issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
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
	runGitCmd(t, remoteDir, "init", "--bare", "-b", "master")

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
		Title:     "E2E test issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
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
	store2.CloseIssue(ctx, issueID, "Done by Agent B", "agent-b", "")
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
	// Note: With slog, we can't easily capture formatted messages like before.
	// For tests that need to verify log output, use strings.Builder and newTestLoggerWithWriter.
	// This helper is kept for backward compatibility but messages won't be captured.
	messages := ""
	return newTestLogger(), &messages
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
	runGitCmd(t, remoteDir, "init", "--bare", "-b", "master")

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

// TestSyncBranchCommitAndPush_WithPreCommitHook is a regression test for the bug where
// daemon auto-sync failed when pre-commit hooks were installed.
//
// Bug: The gitCommitInWorktree function was missing --no-verify flag, causing
// pre-commit hooks to execute in the worktree context. The bd pre-commit hook
// runs "bd sync --flush-only" which fails in a worktree because:
// 1. The worktree's .beads directory triggers hook execution
// 2. But bd sync fails in the worktree context (wrong database path)
// 3. This causes the hook to exit 1, failing the commit
//
// Fix: Add --no-verify to gitCommitInWorktree to skip hooks, matching the
// behavior of the library function in internal/syncbranch/worktree.go
//
// This test verifies that sync branch commits succeed even when a failing
// pre-commit hook is present.
func TestSyncBranchCommitAndPush_WithPreCommitHook(t *testing.T) {
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

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	syncBranch := "beads-sync"
	if err := store.SetConfig(ctx, "sync.branch", syncBranch); err != nil {
		t.Fatalf("Failed to set sync.branch: %v", err)
	}

	// Create a pre-commit hook that simulates the bd pre-commit hook behavior.
	// The actual bd hook runs "bd sync --flush-only" which fails in worktree context.
	// We simulate this by creating a hook that:
	// 1. Checks if .beads directory exists (like bd hook does)
	// 2. If yes, exits with error 1 (simulating bd sync failure)
	// Without --no-verify, this would cause gitCommitInWorktree to fail.
	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("Failed to create hooks dir: %v", err)
	}

	preCommitHook := filepath.Join(hooksDir, "pre-commit")
	hookScript := `#!/bin/sh
# Simulates bd pre-commit hook behavior that fails in worktree context
# The real hook runs "bd sync --flush-only" which fails in worktrees
if [ -d .beads ]; then
    echo "Error: Simulated pre-commit hook failure (bd sync would fail here)" >&2
    exit 1
fi
exit 0
`
	if err := os.WriteFile(preCommitHook, []byte(hookScript), 0755); err != nil {
		t.Fatalf("Failed to write pre-commit hook: %v", err)
	}

	// Add a dummy remote so hasGitRemote() returns true
	// (syncBranchCommitAndPush skips if no remote is configured)
	runGitCmd(t, tmpDir, "remote", "add", "origin", "https://example.com/dummy.git")

	// Create a test issue
	issue := &types.Issue{
		Title:     "Test with pre-commit hook",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	t.Chdir(tmpDir)

	log, logMsgs := newTestSyncBranchLogger()

	// This is the critical test: with the fix (--no-verify), this should succeed.
	// Without the fix, this would fail because the pre-commit hook exits 1.
	committed, err := syncBranchCommitAndPush(ctx, store, false, log)

	if err != nil {
		t.Fatalf("syncBranchCommitAndPush failed with pre-commit hook present: %v\n"+
			"This indicates the --no-verify flag is missing from gitCommitInWorktree.\n"+
			"Logs: %s", err, *logMsgs)
	}
	if !committed {
		t.Error("Expected committed=true with pre-commit hook present")
	}

	// Verify worktree was created
	worktreePath := filepath.Join(tmpDir, ".git", "beads-worktrees", syncBranch)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Errorf("Worktree not created at %s", worktreePath)
	}

	// Verify JSONL was synced to worktree
	worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
	if _, err := os.Stat(worktreeJSONL); os.IsNotExist(err) {
		t.Error("JSONL not synced to worktree")
	}

	// Verify commit was made in worktree
	cmd := exec.Command("git", "-C", worktreePath, "log", "--oneline", "-1")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get log: %v", err)
	}
	if !strings.Contains(string(output), "bd daemon sync") {
		t.Errorf("Expected commit message with 'bd daemon sync', got: %s", string(output))
	}

	// Test multiple commits to ensure hook is consistently bypassed
	for i := 0; i < 3; i++ {
		// Update issue to create new changes
		if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
			"priority": (i % 4) + 1,
		}, "test"); err != nil {
			t.Fatalf("Failed to update issue on iteration %d: %v", i, err)
		}

		if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
			t.Fatalf("Failed to export on iteration %d: %v", i, err)
		}

		committed, err = syncBranchCommitAndPush(ctx, store, false, log)
		if err != nil {
			t.Fatalf("syncBranchCommitAndPush failed on iteration %d: %v", i, err)
		}
		if !committed {
			t.Errorf("Expected committed=true on iteration %d", i)
		}
	}

	// Verify we have multiple commits (initial sync branch commit + 1 initial + 3 updates)
	cmd = exec.Command("git", "-C", worktreePath, "rev-list", "--count", "HEAD")
	output, err = cmd.Output()
	if err != nil {
		t.Fatalf("Failed to count commits: %v", err)
	}
	commitCount := strings.TrimSpace(string(output))
	// At least 4 commits expected (may be more due to sync branch initialization)
	if commitCount == "0" || commitCount == "1" {
		t.Errorf("Expected multiple commits, got %s", commitCount)
	}

	t.Log("Pre-commit hook regression test passed: --no-verify correctly bypasses hooks")
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

// TestGitPushFromWorktree_FetchRebaseRetry tests that gitPushFromWorktree handles
// the case where the remote has newer commits by fetching, rebasing, and retrying.
// This is a regression test for the bug where daemon push would fail with
// "fetch first" error when another clone had pushed to the sync branch.
//
// Bug scenario:
// 1. Clone A pushes commit X to sync branch
// 2. Clone B has local commit Y (not based on X)
// 3. Clone B's push fails with "fetch first" error
// 4. Without this fix: daemon logs failure and stops
// 5. With this fix: daemon fetches, rebases Y on X, and retries push
func TestGitPushFromWorktree_FetchRebaseRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skip on Windows due to path issues
	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows")
	}

	ctx := context.Background()

	// Create a "remote" bare repository
	remoteDir := t.TempDir()
	runGitCmd(t, remoteDir, "init", "--bare", "-b", "master")

	// Create first clone (simulates another developer's clone)
	clone1Dir := t.TempDir()
	runGitCmd(t, clone1Dir, "clone", remoteDir, ".")
	runGitCmd(t, clone1Dir, "config", "user.email", "test@example.com")
	runGitCmd(t, clone1Dir, "config", "user.name", "Test User")

	// Create initial commit on main
	initMainBranch(t, clone1Dir)
	runGitCmd(t, clone1Dir, "push", "-u", "origin", "main")

	// Create sync branch in clone1
	runGitCmd(t, clone1Dir, "checkout", "-b", "beads-sync")
	beadsDir1 := filepath.Join(clone1Dir, ".beads")
	if err := os.MkdirAll(beadsDir1, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	jsonl1 := filepath.Join(beadsDir1, "issues.jsonl")
	if err := os.WriteFile(jsonl1, []byte(`{"id":"clone1-issue","title":"Issue from clone1"}`+"\n"), 0644); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}
	runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
	runGitCmd(t, clone1Dir, "commit", "-m", "Clone 1 commit")
	runGitCmd(t, clone1Dir, "push", "-u", "origin", "beads-sync")

	// Create second clone (simulates our local clone)
	clone2Dir := t.TempDir()
	runGitCmd(t, clone2Dir, "clone", remoteDir, ".")
	runGitCmd(t, clone2Dir, "config", "user.email", "test@example.com")
	runGitCmd(t, clone2Dir, "config", "user.name", "Test User")

	// Create worktree for sync branch in clone2
	worktreePath := filepath.Join(clone2Dir, ".git", "beads-worktrees", "beads-sync")
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		t.Fatalf("Failed to create worktree parent: %v", err)
	}

	// Fetch the sync branch first
	runGitCmd(t, clone2Dir, "fetch", "origin", "beads-sync:beads-sync")

	// Create worktree - but don't pull latest yet (to simulate diverged state)
	runGitCmd(t, clone2Dir, "worktree", "add", worktreePath, "beads-sync")

	// Now clone1 makes another commit and pushes (simulating another clone pushing)
	runGitCmd(t, clone1Dir, "checkout", "beads-sync")
	if err := os.WriteFile(jsonl1, []byte(`{"id":"clone1-issue","title":"Issue from clone1"}`+"\n"+`{"id":"clone1-issue2","title":"Second issue"}`+"\n"), 0644); err != nil {
		t.Fatalf("Failed to update JSONL: %v", err)
	}
	runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
	runGitCmd(t, clone1Dir, "commit", "-m", "Clone 1 second commit")
	runGitCmd(t, clone1Dir, "push", "origin", "beads-sync")

	// Clone2's worktree makes a different commit (diverged from remote)
	// We create a different file to avoid merge conflicts - this simulates
	// non-conflicting JSONL changes (e.g., different issues being created)
	beadsDir2 := filepath.Join(worktreePath, ".beads")
	if err := os.MkdirAll(beadsDir2, 0755); err != nil {
		t.Fatalf("Failed to create .beads in worktree: %v", err)
	}
	// Create a separate metadata file to avoid JSONL conflict
	metadataPath := filepath.Join(beadsDir2, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"clone":"clone2"}`+"\n"), 0644); err != nil {
		t.Fatalf("Failed to write metadata in worktree: %v", err)
	}
	runGitCmd(t, worktreePath, "add", ".beads/metadata.json")
	runGitCmd(t, worktreePath, "commit", "-m", "Clone 2 commit")

	// Now try to push from worktree - this should trigger the fetch-rebase-retry logic
	// because the remote has commits that the local worktree doesn't have
	err := gitPushFromWorktree(ctx, worktreePath, "beads-sync", "")
	if err != nil {
		t.Fatalf("gitPushFromWorktree failed: %v (expected fetch-rebase-retry to succeed)", err)
	}

	// Verify the push succeeded by checking the remote has all commits
	cmd := exec.Command("git", "-C", remoteDir, "rev-list", "--count", "beads-sync")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to count commits: %v", err)
	}
	commitCount := strings.TrimSpace(string(output))
	// Should have at least 3 commits: initial sync, clone1's second commit, clone2's rebased commit
	if commitCount == "0" || commitCount == "1" || commitCount == "2" {
		t.Errorf("Expected at least 3 commits after rebase-push, got %s", commitCount)
	}

	t.Log("Fetch-rebase-retry test passed: diverged sync branch was successfully rebased and pushed")
}

// TestDaemonSyncBranchE2E tests the daemon sync-branch flow with concurrent changes from
// two machines using a real bare repo. This tests the daemon path (syncBranchCommitAndPush/Pull)
// as opposed to TestSyncBranchE2E which tests the CLI path (syncbranch.CommitToSyncBranch/Pull).
//
// Key difference from CLI path tests:
// - CLI: Uses syncbranch.CommitToSyncBranch() from internal/syncbranch
// - Daemon: Uses syncBranchCommitAndPush() from daemon_sync_branch.go
//
// Flow:
// 1. Machine A creates bd-1, calls daemon syncBranchCommitAndPush(), pushes to bare remote
// 2. Machine B creates bd-2, calls daemon syncBranchCommitAndPush(), pushes to bare remote
// 3. Machine A calls daemon syncBranchPull(), should merge both issues
// 4. Verify both issues present after merge
func TestDaemonSyncBranchE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skip on Windows due to path issues with worktrees
	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows")
	}

	ctx := context.Background()

	// Setup: Create bare remote with two clones using Phase 1 helper
	_, machineA, machineB, cleanup := setupBareRemoteWithClones(t)
	defer cleanup()

	// Use unique sync branch name and set via env var (highest priority)
	// This overrides any config.yaml setting
	syncBranch := "beads-daemon-sync"
	t.Setenv(syncbranch.EnvVar, syncBranch)

	// Machine A: Setup database with sync.branch configured
	var storeA *sqlite.SQLiteStorage
	var jsonlPathA string

	withBeadsDir(t, machineA, func() {
		beadsDirA := filepath.Join(machineA, ".beads")
		dbPathA := filepath.Join(beadsDirA, "beads.db")

		var err error
		storeA, err = sqlite.New(ctx, dbPathA)
		if err != nil {
			t.Fatalf("Failed to create store for Machine A: %v", err)
		}

		// Configure store
		if err := storeA.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			t.Fatalf("Failed to set issue_prefix: %v", err)
		}
		if err := storeA.SetConfig(ctx, syncbranch.ConfigKey, syncBranch); err != nil {
			t.Fatalf("Failed to set sync.branch: %v", err)
		}

		// Create issue in Machine A
		issueA := &types.Issue{
			Title:     "Issue from Machine A (daemon path)",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := storeA.CreateIssue(ctx, issueA, "machineA"); err != nil {
			t.Fatalf("Failed to create issue in Machine A: %v", err)
		}
		t.Logf("Machine A created issue: %s", issueA.ID)

		// Export to JSONL
		jsonlPathA = filepath.Join(beadsDirA, "issues.jsonl")
		if err := exportToJSONLWithStore(ctx, storeA, jsonlPathA); err != nil {
			t.Fatalf("Failed to export JSONL for Machine A: %v", err)
		}

		// Change to machineA directory for git operations
		if err := os.Chdir(machineA); err != nil {
			t.Fatalf("Failed to chdir to machineA: %v", err)
		}

		// Set global dbPath so findJSONLPath() works for daemon functions
		oldDBPath := dbPath
		dbPath = dbPathA
		defer func() { dbPath = oldDBPath }()

		// Machine A: Commit and push using daemon path (syncBranchCommitAndPush)
		log, _ := newTestSyncBranchLogger()
		committed, err := syncBranchCommitAndPush(ctx, storeA, true, log)
		if err != nil {
			t.Fatalf("Machine A syncBranchCommitAndPush failed: %v", err)
		}
		if !committed {
			t.Fatal("Expected Machine A daemon commit to succeed")
		}
		t.Log("Machine A: Daemon committed and pushed issue to sync branch")
	})
	defer storeA.Close()

	// Reset git caches before switching to Machine B to prevent path caching issues
	git.ResetCaches()

	// Machine B: Setup database and sync with Machine A's changes first
	var storeB *sqlite.SQLiteStorage
	var jsonlPathB string

	withBeadsDir(t, machineB, func() {
		beadsDirB := filepath.Join(machineB, ".beads")
		dbPathB := filepath.Join(beadsDirB, "beads.db")

		var err error
		storeB, err = sqlite.New(ctx, dbPathB)
		if err != nil {
			t.Fatalf("Failed to create store for Machine B: %v", err)
		}

		// Configure store
		if err := storeB.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			t.Fatalf("Failed to set issue_prefix: %v", err)
		}
		if err := storeB.SetConfig(ctx, syncbranch.ConfigKey, syncBranch); err != nil {
			t.Fatalf("Failed to set sync.branch: %v", err)
		}

		jsonlPathB = filepath.Join(beadsDirB, "issues.jsonl")

		// Change to machineB directory for git operations
		if err := os.Chdir(machineB); err != nil {
			t.Fatalf("Failed to chdir to machineB: %v", err)
		}

		// Set global dbPath so findJSONLPath() works for daemon functions
		oldDBPath := dbPath
		dbPath = dbPathB
		defer func() { dbPath = oldDBPath }()

		// Machine B: First pull from sync branch to get Machine A's issue
		// This is the correct workflow - always pull before creating local changes
		log, _ := newTestSyncBranchLogger()
		pulled, err := syncBranchPull(ctx, storeB, log)
		if err != nil {
			t.Logf("Machine B initial pull error (may be expected): %v", err)
		}
		if pulled {
			t.Log("Machine B: Pulled Machine A's changes from sync branch")
		}

		// Import the pulled JSONL into Machine B's database
		if _, err := os.Stat(jsonlPathB); err == nil {
			if err := importToJSONLWithStore(ctx, storeB, jsonlPathB); err != nil {
				t.Logf("Machine B import warning: %v", err)
			}
		}

		// Create issue in Machine B (different from A)
		issueB := &types.Issue{
			Title:     "Issue from Machine B (daemon path)",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeBug,
			CreatedAt: time.Now().Add(time.Second), // Ensure different timestamp
			UpdatedAt: time.Now().Add(time.Second),
		}
		if err := storeB.CreateIssue(ctx, issueB, "machineB"); err != nil {
			t.Fatalf("Failed to create issue in Machine B: %v", err)
		}
		t.Logf("Machine B created issue: %s", issueB.ID)

		// Export to JSONL (now includes both Machine A's and Machine B's issues)
		if err := exportToJSONLWithStore(ctx, storeB, jsonlPathB); err != nil {
			t.Fatalf("Failed to export JSONL for Machine B: %v", err)
		}

		// Machine B: Commit and push using daemon path
		// This should succeed without conflict because we pulled first
		committed, err := syncBranchCommitAndPush(ctx, storeB, true, log)
		if err != nil {
			t.Fatalf("Machine B syncBranchCommitAndPush failed: %v", err)
		}
		if !committed {
			t.Fatal("Expected Machine B daemon commit to succeed")
		}
		t.Log("Machine B: Daemon committed and pushed issue to sync branch")
	})
	defer storeB.Close()

	// Reset git caches before switching back to Machine A
	git.ResetCaches()

	// Machine A: Pull from sync branch using daemon path
	withBeadsDir(t, machineA, func() {
		beadsDirA := filepath.Join(machineA, ".beads")
		dbPathA := filepath.Join(beadsDirA, "beads.db")

		// Change to machineA directory for git operations
		if err := os.Chdir(machineA); err != nil {
			t.Fatalf("Failed to chdir to machineA: %v", err)
		}

		// Set global dbPath so findJSONLPath() works for daemon functions
		oldDBPath := dbPath
		dbPath = dbPathA
		defer func() { dbPath = oldDBPath }()

		log, _ := newTestSyncBranchLogger()
		pulled, err := syncBranchPull(ctx, storeA, log)
		if err != nil {
			t.Fatalf("Machine A syncBranchPull failed: %v", err)
		}
		if !pulled {
			t.Log("Machine A syncBranchPull returned false (may be expected if no remote changes)")
		} else {
			t.Log("Machine A: Daemon pulled from sync branch")
		}
	})

	// Verify: Both issues should be present in Machine A's JSONL after merge
	content, err := os.ReadFile(jsonlPathA)
	if err != nil {
		t.Fatalf("Failed to read Machine A's JSONL: %v", err)
	}

	contentStr := string(content)
	hasMachineA := strings.Contains(contentStr, "Machine A")
	hasMachineB := strings.Contains(contentStr, "Machine B")

	if hasMachineA {
		t.Log("Issue from Machine A preserved in JSONL")
	} else {
		t.Error("FAIL: Issue from Machine A missing after merge")
	}

	if hasMachineB {
		t.Log("Issue from Machine B merged into JSONL")
	} else {
		t.Error("FAIL: Issue from Machine B missing after merge")
	}

	if hasMachineA && hasMachineB {
		t.Log("Daemon sync-branch E2E test PASSED: both issues present after merge")
	}

	// Clean up git caches to prevent test pollution
	git.ResetCaches()
}

// TestDaemonSyncBranchForceOverwrite tests the forceOverwrite flag behavior for delete mutations.
// When forceOverwrite is true, the local JSONL is copied directly to the worktree without merging,
// which is necessary for delete mutations to be properly reflected in the sync branch.
//
// Flow:
// 1. Machine A creates issue, commits to sync branch
// 2. Machine A deletes issue locally, calls syncBranchCommitAndPushWithOptions(forceOverwrite=true)
// 3. Verify the deletion is reflected in the sync branch worktree
func TestDaemonSyncBranchForceOverwrite(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skip on Windows due to path issues with worktrees
	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows")
	}

	ctx := context.Background()

	// Setup: Create bare remote with two clones
	_, machineA, _, cleanup := setupBareRemoteWithClones(t)
	defer cleanup()

	// Use unique sync branch name and set via env var (highest priority)
	// This overrides any config.yaml setting
	syncBranch := "beads-force-sync"
	t.Setenv(syncbranch.EnvVar, syncBranch)

	withBeadsDir(t, machineA, func() {
		beadsDirA := filepath.Join(machineA, ".beads")
		dbPathA := filepath.Join(beadsDirA, "beads.db")

		storeA, err := sqlite.New(ctx, dbPathA)
		if err != nil {
			t.Fatalf("Failed to create store: %v", err)
		}
		defer storeA.Close()

		// Configure store
		if err := storeA.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			t.Fatalf("Failed to set issue_prefix: %v", err)
		}
		if err := storeA.SetConfig(ctx, syncbranch.ConfigKey, syncBranch); err != nil {
			t.Fatalf("Failed to set sync.branch: %v", err)
		}

		// Create two issues
		issue1 := &types.Issue{
			Title:     "Issue to keep",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := storeA.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}

		issue2 := &types.Issue{
			Title:     "Issue to delete",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := storeA.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}
		issue2ID := issue2.ID
		t.Logf("Created issue to delete: %s", issue2ID)

		// Export to JSONL
		jsonlPath := filepath.Join(beadsDirA, "issues.jsonl")
		if err := exportToJSONLWithStore(ctx, storeA, jsonlPath); err != nil {
			t.Fatalf("Failed to export JSONL: %v", err)
		}

		// Change to machineA directory for git operations
		if err := os.Chdir(machineA); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}

		// Set global dbPath so findJSONLPath() works for daemon functions
		oldDBPath := dbPath
		dbPath = dbPathA
		defer func() { dbPath = oldDBPath }()

		// First commit with both issues (without forceOverwrite)
		log, _ := newTestSyncBranchLogger()
		committed, err := syncBranchCommitAndPush(ctx, storeA, true, log)
		if err != nil {
			t.Fatalf("Initial commit failed: %v", err)
		}
		if !committed {
			t.Fatal("Expected initial commit to succeed")
		}
		t.Log("Initial commit with both issues succeeded")

		// Verify worktree has both issues
		worktreePath := filepath.Join(machineA, ".git", "beads-worktrees", syncBranch)
		worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
		initialContent, err := os.ReadFile(worktreeJSONL)
		if err != nil {
			t.Fatalf("Failed to read worktree JSONL: %v", err)
		}
		if !strings.Contains(string(initialContent), "Issue to delete") {
			t.Error("Initial worktree JSONL should contain 'Issue to delete'")
		}

		// Now delete the issue from database
		if err := storeA.DeleteIssue(ctx, issue2ID); err != nil {
			t.Fatalf("Failed to delete issue: %v", err)
		}
		t.Logf("Deleted issue %s from database", issue2ID)

		// Export JSONL after deletion (issue2 should not be in the file)
		if err := exportToJSONLWithStore(ctx, storeA, jsonlPath); err != nil {
			t.Fatalf("Failed to export JSONL after deletion: %v", err)
		}

		// Verify local JSONL no longer has the deleted issue
		localContent, err := os.ReadFile(jsonlPath)
		if err != nil {
			t.Fatalf("Failed to read local JSONL: %v", err)
		}
		if strings.Contains(string(localContent), "Issue to delete") {
			t.Error("Local JSONL should not contain deleted issue")
		}

		// Commit with forceOverwrite=true (simulating delete mutation)
		committed, err = syncBranchCommitAndPushWithOptions(ctx, storeA, true, true, log)
		if err != nil {
			t.Fatalf("forceOverwrite commit failed: %v", err)
		}
		if !committed {
			t.Fatal("Expected forceOverwrite commit to succeed")
		}
		t.Log("forceOverwrite commit succeeded")

		// Verify worktree JSONL no longer has the deleted issue
		afterContent, err := os.ReadFile(worktreeJSONL)
		if err != nil {
			t.Fatalf("Failed to read worktree JSONL after forceOverwrite: %v", err)
		}

		if strings.Contains(string(afterContent), "Issue to delete") {
			t.Error("FAIL: Worktree JSONL still contains deleted issue after forceOverwrite")
		} else {
			t.Log("Worktree JSONL correctly reflects deletion after forceOverwrite")
		}

		if !strings.Contains(string(afterContent), "Issue to keep") {
			t.Error("FAIL: Worktree JSONL should still contain 'Issue to keep'")
		} else {
			t.Log("Worktree JSONL correctly preserves non-deleted issue")
		}

		t.Log("forceOverwrite test PASSED: delete mutation correctly propagated")
	})

	// Clean up git caches to prevent test pollution
	git.ResetCaches()
}

// TestDaemonExportSkipsSameBranch tests that daemon export is skipped when
// sync-branch == current-branch. This prevents the worktree conflict issue (GH#1258).
func TestDaemonExportSkipsSameBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up git caches to avoid pollution from previous tests
	git.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Get current branch name
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))

	// Set BEADS_SYNC_BRANCH to the current branch (highest priority config)
	// This is the bug case: sync-branch == current-branch
	t.Setenv(syncbranch.EnvVar, currentBranch)

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
		Title:     "Test same branch guard",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL (initial export for setup)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call performExport (via createExportFunc) - this should be skipped
	exportFn := createExportFunc(ctx, store, true, false, log)
	exportFn()

	// Verify the operation was skipped by checking log output
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, "sync-branch") {
		t.Errorf("Expected log to contain skip message about sync-branch, got:\n%s", logOutput)
	}

	// Verify the log mentions the current branch
	if !strings.Contains(logOutput, currentBranch) {
		t.Errorf("Expected log to mention branch '%s', got:\n%s", currentBranch, logOutput)
	}

	// Verify "Starting export" was NOT logged (guard returned early)
	if strings.Contains(logOutput, "Starting export") {
		t.Error("Expected export to be skipped before 'Starting export' was logged")
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonAutoImportSkipsSameBranch tests that daemon auto-import is skipped when
// sync-branch == current-branch. This prevents the worktree conflict issue (GH#1258).
func TestDaemonAutoImportSkipsSameBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Get current branch name
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))

	// Set BEADS_SYNC_BRANCH to the current branch (highest priority config)
	// This is the bug case: sync-branch == current-branch
	t.Setenv(syncbranch.EnvVar, currentBranch)

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

	// Create JSONL file (needed for auto-import)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test issue"}`+"\n"), 0644); err != nil {
		t.Fatalf("Failed to write JSONL: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call performAutoImport (via createAutoImportFunc) - this should be skipped
	importFn := createAutoImportFunc(ctx, store, log)
	importFn()

	// Verify the operation was skipped by checking log output
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, "sync-branch") {
		t.Errorf("Expected log to contain skip message about sync-branch, got:\n%s", logOutput)
	}

	// Verify the log mentions the current branch
	if !strings.Contains(logOutput, currentBranch) {
		t.Errorf("Expected log to mention branch '%s', got:\n%s", currentBranch, logOutput)
	}

	// Verify "Starting auto-import" was NOT logged (guard returned early)
	if strings.Contains(logOutput, "Starting auto-import") {
		t.Error("Expected auto-import to be skipped before 'Starting auto-import' was logged")
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonSyncSkipsSameBranch tests that daemon sync cycle is skipped when
// sync-branch == current-branch. This prevents the worktree conflict issue (GH#1258).
func TestDaemonSyncSkipsSameBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Get current branch name
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))

	// Set BEADS_SYNC_BRANCH to the current branch (highest priority config)
	// This is the bug case: sync-branch == current-branch
	t.Setenv(syncbranch.EnvVar, currentBranch)

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
		Title:     "Test same branch guard",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	// Export to JSONL (initial export for setup)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call performSync (via createSyncFunc) - this should be skipped
	syncFn := createSyncFunc(ctx, store, true, false, log)
	syncFn()

	// Verify the operation was skipped by checking log output
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, "sync-branch") {
		t.Errorf("Expected log to contain skip message about sync-branch, got:\n%s", logOutput)
	}

	// Verify the log mentions the current branch
	if !strings.Contains(logOutput, currentBranch) {
		t.Errorf("Expected log to mention branch '%s', got:\n%s", currentBranch, logOutput)
	}

	// Verify "Starting sync cycle" was NOT logged (guard returned early)
	if strings.Contains(logOutput, "Starting sync cycle") {
		t.Error("Expected sync cycle to be skipped before 'Starting sync cycle' was logged")
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestSyncBranchCommitSkipsSameBranch tests that syncBranchCommitAndPushWithOptions
// is skipped when sync-branch == current-branch. This prevents the worktree conflict issue (GH#1258).
func TestSyncBranchCommitSkipsSameBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Add a dummy remote so hasGitRemote() returns true
	runGitCmd(t, tmpDir, "remote", "add", "origin", "https://example.com/dummy.git")

	// Get current branch name
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))

	// Set BEADS_SYNC_BRANCH to the current branch (highest priority config)
	// This is the bug case: sync-branch == current-branch
	t.Setenv(syncbranch.EnvVar, currentBranch)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create test issue and export to JSONL
	issue := &types.Issue{
		Title:     "Test same branch guard",
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

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call syncBranchCommitAndPushWithOptions - this should be skipped
	committed, err := syncBranchCommitAndPushWithOptions(ctx, store, false, false, log)

	// Should return false (not committed), no error
	if err != nil {
		t.Errorf("Expected no error when same branch detected, got: %v", err)
	}
	if committed {
		t.Error("Expected committed=false when same branch detected")
	}

	// Verify the operation was skipped by checking log output
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, "sync-branch") {
		t.Errorf("Expected log to contain skip message about sync-branch, got:\n%s", logOutput)
	}

	// Verify the log mentions the current branch
	if !strings.Contains(logOutput, currentBranch) {
		t.Errorf("Expected log to mention branch '%s', got:\n%s", currentBranch, logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestSyncBranchPullSkipsSameBranch tests that syncBranchPull is skipped when
// sync-branch == current-branch. This prevents the worktree conflict issue (GH#1258).
func TestSyncBranchPullSkipsSameBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Add a dummy remote so hasGitRemote() returns true
	runGitCmd(t, tmpDir, "remote", "add", "origin", "https://example.com/dummy.git")

	// Get current branch name
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))

	// Set BEADS_SYNC_BRANCH to the current branch (highest priority config)
	// This is the bug case: sync-branch == current-branch
	t.Setenv(syncbranch.EnvVar, currentBranch)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call syncBranchPull - this should be skipped
	pulled, err := syncBranchPull(ctx, store, log)

	// Should return false (not pulled), no error
	if err != nil {
		t.Errorf("Expected no error when same branch detected, got: %v", err)
	}
	if pulled {
		t.Error("Expected pulled=false when same branch detected")
	}

	// Verify the operation was skipped by checking log output
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, "sync-branch") {
		t.Errorf("Expected log to contain skip message about sync-branch, got:\n%s", logOutput)
	}

	// Verify the log mentions the current branch
	if !strings.Contains(logOutput, currentBranch) {
		t.Errorf("Expected log to mention branch '%s', got:\n%s", currentBranch, logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// ============================================================================
// Edge Case Tests (Scenarios 8-12 from test matrix)
// ============================================================================

// TestDaemonExportWorktreeDifferentBranch (scenario 8) tests that operations are
// ALLOWED when working in a git worktree that is on a different branch than sync-branch.
func TestDaemonExportWorktreeDifferentBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows due to worktree path issues")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Create a feature branch and a worktree for it
	runGitCmd(t, tmpDir, "branch", "feature-branch")
	worktreePath := filepath.Join(tmpDir, "feature-worktree")
	runGitCmd(t, tmpDir, "worktree", "add", worktreePath, "feature-branch")

	// Change to the worktree (which is on feature-branch)
	t.Chdir(worktreePath)

	// Set BEADS_SYNC_BRANCH to a DIFFERENT branch (main or master)
	// This should ALLOW operations because feature-branch != main
	t.Setenv(syncbranch.EnvVar, "main")

	// Setup test store in the worktree
	beadsDir := filepath.Join(worktreePath, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create test issue and export to JSONL
	issue := &types.Issue{
		Title:     "Test worktree different branch",
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

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call shouldSkipDueToSameBranch directly - should return false (allow)
	shouldSkip := shouldSkipDueToSameBranch(ctx, store, "test-operation", log)

	if shouldSkip {
		t.Error("Expected operation to be ALLOWED when worktree is on different branch than sync-branch")
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "Skipping") {
		t.Errorf("Expected no skip message, got:\n%s", logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonExportWorktreeSameBranch (scenario 9) tests that operations are
// BLOCKED when working in a git worktree that is on the same branch as sync-branch.
func TestDaemonExportWorktreeSameBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows due to worktree path issues")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Create a sync branch and a worktree for it
	syncBranch := "beads-sync"
	runGitCmd(t, tmpDir, "branch", syncBranch)
	worktreePath := filepath.Join(tmpDir, "sync-worktree")
	runGitCmd(t, tmpDir, "worktree", "add", worktreePath, syncBranch)

	// Change to the worktree (which is on the sync branch)
	t.Chdir(worktreePath)

	// Set BEADS_SYNC_BRANCH to the SAME branch as the worktree
	// This should BLOCK operations because beads-sync == beads-sync
	t.Setenv(syncbranch.EnvVar, syncBranch)

	// Setup test store in the worktree
	beadsDir := filepath.Join(worktreePath, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call shouldSkipDueToSameBranch directly - should return true (block)
	shouldSkip := shouldSkipDueToSameBranch(ctx, store, "test-operation", log)

	if !shouldSkip {
		t.Error("Expected operation to be BLOCKED when worktree is on same branch as sync-branch")
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, syncBranch) {
		t.Errorf("Expected skip message mentioning '%s', got:\n%s", syncBranch, logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonExportDynamicBranchSwitch (scenario 10) tests that the guard correctly
// detects when a user switches to the sync branch after daemon starts.
func TestDaemonExportDynamicBranchSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Start on main branch
	t.Chdir(tmpDir)

	// Create a sync branch
	syncBranch := "beads-sync"
	runGitCmd(t, tmpDir, "branch", syncBranch)

	// Set BEADS_SYNC_BRANCH
	t.Setenv(syncbranch.EnvVar, syncBranch)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// First check on main branch - should allow
	shouldSkip := shouldSkipDueToSameBranch(ctx, store, "test-operation", log)
	if shouldSkip {
		t.Error("Expected operation to be allowed on main branch")
	}

	// Reset caches to simulate fresh state after branch switch
	git.ResetCaches()
	logBuf.Reset()

	// Switch to the sync branch (simulating user's `git checkout beads-sync`)
	runGitCmd(t, tmpDir, "checkout", syncBranch)

	// Reset caches again to ensure fresh branch detection
	git.ResetCaches()

	// Now check again - should block (dynamic detection)
	shouldSkip = shouldSkipDueToSameBranch(ctx, store, "test-operation", log)
	if !shouldSkip {
		t.Error("Expected operation to be BLOCKED after switching to sync branch")
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, syncBranch) {
		t.Errorf("Expected skip message after branch switch, got:\n%s", logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonExportAfterBranchChange (scenario 11) tests that operations are
// ALLOWED after user switches away from the sync branch.
func TestDaemonExportAfterBranchChange(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Start on main branch and create a sync branch
	t.Chdir(tmpDir)
	syncBranch := "beads-sync"
	runGitCmd(t, tmpDir, "branch", syncBranch)

	// Set BEADS_SYNC_BRANCH
	t.Setenv(syncbranch.EnvVar, syncBranch)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Switch to sync branch (should block)
	runGitCmd(t, tmpDir, "checkout", syncBranch)
	git.ResetCaches()

	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	shouldSkip := shouldSkipDueToSameBranch(ctx, store, "test-operation", log)
	if !shouldSkip {
		t.Error("Expected operation to be blocked on sync branch")
	}

	// Now switch back to main (should allow)
	runGitCmd(t, tmpDir, "checkout", "main")
	git.ResetCaches()
	logBuf.Reset()

	shouldSkip = shouldSkipDueToSameBranch(ctx, store, "test-operation", log)
	if shouldSkip {
		t.Error("Expected operation to be ALLOWED after switching away from sync branch")
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "Skipping") {
		t.Errorf("Expected no skip message after switching to main, got:\n%s", logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonExportConfigReload (scenario 12) tests that the guard correctly
// detects when sync.branch config is changed via environment variable.
// Note: We test via env var because syncbranch.Get() has a priority order:
// 1. BEADS_SYNC_BRANCH env var (highest)
// 2. config.yaml (may be polluted from other tests)
// 3. database config (lowest)
// Using env var ensures we're testing the actual reload behavior.
func TestDaemonExportConfigReload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up caches to avoid pollution from previous tests
	git.ResetCaches()
	beads.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	t.Chdir(tmpDir)

	// Get current branch name
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set global dbPath so findJSONLPath() works
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()
	dbPath = testDBPath

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Set sync.branch to a DIFFERENT branch via env var (safe-sync)
	t.Setenv(syncbranch.EnvVar, "safe-sync")

	// First check - should allow (main != safe-sync)
	shouldSkip := shouldSkipDueToSameBranch(ctx, store, "test-operation", log)
	if shouldSkip {
		t.Error("Expected operation to be allowed when sync.branch is 'safe-sync'")
	}

	// Now change sync.branch to the current branch via env var (simulating config reload)
	t.Setenv(syncbranch.EnvVar, currentBranch)
	logBuf.Reset()

	// Second check - should block (main == main)
	shouldSkip = shouldSkipDueToSameBranch(ctx, store, "test-operation", log)
	if !shouldSkip {
		t.Error("Expected operation to be BLOCKED after sync.branch changed to current branch")
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Skipping") || !strings.Contains(logOutput, currentBranch) {
		t.Errorf("Expected skip message after config change, got:\n%s", logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonStartupWarnsSameBranch tests that daemon startup logs a warning when
// sync-branch == current-branch, but continues to start (warn only, don't block).
// This is a one-time warning at startup (GH#1258 Phase 3).
func TestDaemonStartupWarnsSameBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up git caches to avoid pollution from previous tests
	git.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Get current branch name
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))

	// Set BEADS_SYNC_BRANCH to the current branch (highest priority config)
	// This is the misconfiguration case: sync-branch == current-branch
	t.Setenv(syncbranch.EnvVar, currentBranch)

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call warnIfSyncBranchMisconfigured - this should log a warning
	misconfigured := warnIfSyncBranchMisconfigured(ctx, store, log)

	// Verify the function returns true (misconfigured)
	if !misconfigured {
		t.Error("Expected warnIfSyncBranchMisconfigured to return true")
	}

	// Verify the warning was logged
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "sync-branch misconfiguration detected") {
		t.Errorf("Expected warning about sync-branch misconfiguration, got:\n%s", logOutput)
	}

	// Verify the log mentions the branch name
	if !strings.Contains(logOutput, currentBranch) {
		t.Errorf("Expected warning to mention branch '%s', got:\n%s", currentBranch, logOutput)
	}

	// Verify the log contains guidance about dedicated sync branch
	if !strings.Contains(logOutput, "beads-sync") {
		t.Errorf("Expected warning to suggest 'beads-sync', got:\n%s", logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonStartupNoWarningWhenDifferentBranch tests that daemon startup does NOT
// log a warning when sync-branch is configured but different from current-branch.
func TestDaemonStartupNoWarningWhenDifferentBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up git caches to avoid pollution from previous tests
	git.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Set BEADS_SYNC_BRANCH to a different branch (not current-branch)
	t.Setenv(syncbranch.EnvVar, "beads-sync")

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call warnIfSyncBranchMisconfigured - this should NOT log a warning
	misconfigured := warnIfSyncBranchMisconfigured(ctx, store, log)

	// Verify the function returns false (not misconfigured)
	if misconfigured {
		t.Error("Expected warnIfSyncBranchMisconfigured to return false")
	}

	// Verify NO warning was logged
	logOutput := logBuf.String()
	if strings.Contains(logOutput, "misconfiguration") {
		t.Errorf("Expected NO warning when sync-branch differs from current-branch, got:\n%s", logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}

// TestDaemonStartupNoWarningWhenNoSyncBranch tests that daemon startup does NOT
// log a warning when no sync-branch is configured.
func TestDaemonStartupNoWarningWhenNoSyncBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Clean up git caches to avoid pollution from previous tests
	git.ResetCaches()

	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	initMainBranch(t, tmpDir)

	// Change to temp directory first (so git commands work)
	t.Chdir(tmpDir)

	// Ensure no sync-branch is configured (unset env var)
	t.Setenv(syncbranch.EnvVar, "")

	// Setup test store
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(context.Background(), testDBPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create logger that captures output
	var logBuf strings.Builder
	log := newTestLoggerWithWriter(&logBuf)

	// Call warnIfSyncBranchMisconfigured - this should NOT log a warning
	misconfigured := warnIfSyncBranchMisconfigured(ctx, store, log)

	// Verify the function returns false (not misconfigured)
	if misconfigured {
		t.Error("Expected warnIfSyncBranchMisconfigured to return false when no sync-branch configured")
	}

	// Verify NO warning was logged
	logOutput := logBuf.String()
	if strings.Contains(logOutput, "misconfiguration") {
		t.Errorf("Expected NO warning when no sync-branch configured, got:\n%s", logOutput)
	}

	// Clean up git caches
	git.ResetCaches()
}
