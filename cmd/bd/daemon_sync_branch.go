package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// syncBranchCommitAndPush commits JSONL to the sync branch using a worktree
// Returns true if changes were committed, false if no changes or sync.branch not configured
func syncBranchCommitAndPush(ctx context.Context, store storage.Storage, autoPush bool, log daemonLogger) (bool, error) {
	// Check if any remote exists (bd-biwp: support local-only repos)
	if !hasGitRemote(ctx) {
		return true, nil // Skip sync branch commit/push in local-only mode
	}
	
	// Get sync branch configuration (supports BEADS_SYNC_BRANCH override)
	syncBranch, err := syncbranch.Get(ctx, store)
	if err != nil {
		return false, fmt.Errorf("failed to get sync branch: %w", err)
	}

	// If no sync branch configured, caller should use regular commit logic
	if syncBranch == "" {
		return false, nil
	}
	
	log.log("Using sync branch: %s", syncBranch)
	
	// Get repo root
	repoRoot, err := getGitRoot(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get git root: %w", err)
	}
	
	// Worktree path is under .git/beads-worktrees/<branch>
	worktreePath := filepath.Join(repoRoot, ".git", "beads-worktrees", syncBranch)
	
	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)
	
	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return false, fmt.Errorf("failed to create worktree: %w", err)
	}
	
	// Check worktree health and repair if needed
	if err := wtMgr.CheckWorktreeHealth(worktreePath); err != nil {
		log.log("Worktree health check failed, attempting repair: %v", err)
		// Try to recreate worktree
		if err := wtMgr.RemoveBeadsWorktree(worktreePath); err != nil {
			log.log("Failed to remove unhealthy worktree: %v", err)
		}
		if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
			return false, fmt.Errorf("failed to recreate worktree after health check: %w", err)
		}
	}
	
	// Sync JSONL file to worktree
	// Get the actual JSONL path (could be issues.jsonl, beads.base.jsonl, etc.)
	jsonlPath := findJSONLPath()
	if jsonlPath == "" {
		return false, fmt.Errorf("JSONL path not found")
	}
	
	// Convert absolute path to relative path from repo root
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return false, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}
	
	if err := wtMgr.SyncJSONLToWorktree(worktreePath, jsonlRelPath); err != nil {
		return false, fmt.Errorf("failed to sync JSONL to worktree: %w", err)
	}
	
	// Check for changes in worktree
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	hasChanges, err := gitHasChangesInWorktree(ctx, worktreePath, worktreeJSONLPath)
	if err != nil {
		return false, fmt.Errorf("failed to check for changes in worktree: %w", err)
	}
	
	if !hasChanges {
		log.log("No changes to commit in sync branch")
		return false, nil
	}
	
	// Commit in worktree
	message := fmt.Sprintf("bd daemon sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	if err := gitCommitInWorktree(ctx, worktreePath, worktreeJSONLPath, message); err != nil {
		return false, fmt.Errorf("failed to commit in worktree: %w", err)
	}
	log.log("Committed changes to sync branch %s", syncBranch)
	
	// Push if enabled
	if autoPush {
		if err := gitPushFromWorktree(ctx, worktreePath, syncBranch); err != nil {
			return false, fmt.Errorf("failed to push from worktree: %w", err)
		}
		log.log("Pushed sync branch %s to remote", syncBranch)
	}
	
	return true, nil
}

// getGitRoot returns the git repository root directory
func getGitRoot(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git root: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// gitHasChangesInWorktree checks if there are changes in the worktree
func gitHasChangesInWorktree(ctx context.Context, worktreePath, filePath string) (bool, error) {
	// Make filePath relative to worktree
	relPath, err := filepath.Rel(worktreePath, filePath)
	if err != nil {
		return false, fmt.Errorf("failed to make path relative: %w", err)
	}
	
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain", relPath) // #nosec G204 - worktreePath and relPath are derived from trusted git operations
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed in worktree: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// gitCommitInWorktree commits changes in the worktree
func gitCommitInWorktree(ctx context.Context, worktreePath, filePath, message string) error {
	// Make filePath relative to worktree
	relPath, err := filepath.Rel(worktreePath, filePath)
	if err != nil {
		return fmt.Errorf("failed to make path relative: %w", err)
	}
	
	// Stage the file
	addCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", relPath) // #nosec G204 - worktreePath and relPath are derived from trusted git operations
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed in worktree: %w", err)
	}
	
	// Commit
	commitCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "commit", "-m", message)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed in worktree: %w\n%s", err, output)
	}
	
	return nil
}

// gitPushFromWorktree pushes the sync branch from the worktree
func gitPushFromWorktree(ctx context.Context, worktreePath, branch string) error {
	// Get remote name (usually "origin")
	remoteCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) // #nosec G204 - worktreePath and branch are from config
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		// If no remote configured, default to "origin" and set up tracking
		remoteOutput = []byte("origin\n")
	}
	remote := strings.TrimSpace(string(remoteOutput))
	
	// Push with explicit remote and branch, set upstream if not set
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "--set-upstream", remote, branch) // #nosec G204 - worktreePath, remote, and branch are from config
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed from worktree: %w\n%s", err, output)
	}
	
	return nil
}

// syncBranchPull pulls changes from the sync branch into the worktree
// Returns true if pull was performed, false if sync.branch not configured
func syncBranchPull(ctx context.Context, store storage.Storage, log daemonLogger) (bool, error) {
	// Check if any remote exists (bd-biwp: support local-only repos)
	if !hasGitRemote(ctx) {
		return true, nil // Skip sync branch pull in local-only mode
	}
	
	// Get sync branch configuration (supports BEADS_SYNC_BRANCH override)
	syncBranch, err := syncbranch.Get(ctx, store)
	if err != nil {
		return false, fmt.Errorf("failed to get sync branch: %w", err)
	}

	// If no sync branch configured, caller should use regular pull logic
	if syncBranch == "" {
		return false, nil
	}
	
	// Get repo root
	repoRoot, err := getGitRoot(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get git root: %w", err)
	}
	
	// Worktree path is under .git/beads-worktrees/<branch>
	worktreePath := filepath.Join(repoRoot, ".git", "beads-worktrees", syncBranch)
	
	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)
	
	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return false, fmt.Errorf("failed to create worktree: %w", err)
	}
	
	// Get remote name
	remoteCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "config", "--get", fmt.Sprintf("branch.%s.remote", syncBranch)) // #nosec G204 - worktreePath and syncBranch are from config
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		// If no remote configured, default to "origin"
		remoteOutput = []byte("origin\n")
	}
	remote := strings.TrimSpace(string(remoteOutput))
	
	// Pull in worktree
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "pull", remote, syncBranch) // #nosec G204 - worktreePath, remote, and syncBranch are from config
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git pull failed in worktree: %w\n%s", err, output)
	}
	
	log.log("Pulled sync branch %s", syncBranch)
	
	// Get the actual JSONL path
	jsonlPath := findJSONLPath()
	if jsonlPath == "" {
		return false, fmt.Errorf("JSONL path not found")
	}
	
	// Convert to relative path
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return false, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}
	
	// Copy JSONL back to main repo
	worktreeJSONLPath := filepath.Join(worktreePath, jsonlRelPath)
	mainJSONLPath := jsonlPath
	
	// Check if worktree JSONL exists
	if _, err := os.Stat(worktreeJSONLPath); os.IsNotExist(err) {
		// No JSONL in worktree yet, nothing to sync
		return true, nil
	}
	
	// Copy JSONL from worktree to main repo
	data, err := os.ReadFile(worktreeJSONLPath) // #nosec G304 - path is derived from trusted git worktree
	if err != nil {
		return false, fmt.Errorf("failed to read worktree JSONL: %w", err)
	}

	if err := os.WriteFile(mainJSONLPath, data, 0644); err != nil { // #nosec G306 - JSONL needs to be readable
		return false, fmt.Errorf("failed to write main JSONL: %w", err)
	}
	
	log.log("Synced JSONL from sync branch to main repo")
	
	return true, nil
}
