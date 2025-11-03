package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
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
	store, err := sqlite.New(dbPath)
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
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Test with no sync.branch configured
	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	committed, err := syncBranchCommitAndPush(ctx, store, jsonlPath, false, log)

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
	store, err := sqlite.New(dbPath)
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
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

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
	committed, err := syncBranchCommitAndPush(ctx, store, jsonlPath, false, log)

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
	store, err := sqlite.New(dbPath)
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

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	log, logMsgs := newTestSyncBranchLogger()

	// First commit should succeed
	committed, err := syncBranchCommitAndPush(ctx, store, jsonlPath, false, log)
	if err != nil {
		t.Fatalf("First commit failed: %v", err)
	}
	if !committed {
		t.Error("Expected first commit to succeed")
	}

	// Second commit with no changes should return false
	committed, err = syncBranchCommitAndPush(ctx, store, jsonlPath, false, log)
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
	store, err := sqlite.New(dbPath)
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

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	log, logMsgs := newTestSyncBranchLogger()

	// First commit to create worktree
	committed, err := syncBranchCommitAndPush(ctx, store, jsonlPath, false, log)
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
	committed, err = syncBranchCommitAndPush(ctx, store, jsonlPath, false, log)
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
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

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
	store1, err := sqlite.New(clone1DBPath)
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
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	if err := os.Chdir(clone1Dir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Push to sync branch using syncBranchCommitAndPush
	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	committed, err := syncBranchCommitAndPush(ctx, store1, clone1JSONLPath, true, log)
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
	store2, err := sqlite.New(clone2DBPath)
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
	if err := os.Chdir(clone2Dir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

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
	store1, _ := sqlite.New(clone1DBPath)
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
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(clone1Dir)

	// Agent A commits to sync branch
	log, logMsgs := newTestSyncBranchLogger()
	_ = logMsgs // unused in this test
	committed, err := syncBranchCommitAndPush(ctx, store1, clone1JSONLPath, true, log)
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
	store2, _ := sqlite.New(clone2DBPath)
	defer store2.Close()

	store2.SetConfig(ctx, "issue_prefix", "test")
	store2.SetConfig(ctx, "sync.branch", syncBranch)

	// Change to clone2 directory
	os.Chdir(clone2Dir)

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
	committed, err = syncBranchCommitAndPush(ctx, store2, clone2JSONLPath, true, log2)
	if err != nil {
		t.Fatalf("syncBranchCommitAndPush failed for clone2: %v", err)
	}
	if !committed {
		t.Error("Expected commit to succeed for clone2")
	}

	// Agent A pulls the update
	os.Chdir(clone1Dir)
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
