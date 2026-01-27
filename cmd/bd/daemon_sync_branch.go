package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// syncBranchCommitAndPush commits JSONL to the sync branch using a worktree.
// Returns true if changes were committed, false if no changes or sync.branch not configured.
// This is a convenience wrapper that calls syncBranchCommitAndPushWithOptions with default options.
func syncBranchCommitAndPush(ctx context.Context, store storage.Storage, autoPush bool, log daemonLogger) (bool, error) {
	return syncBranchCommitAndPushWithOptions(ctx, store, autoPush, false, log)
}

// syncBranchCommitAndPushWithOptions commits JSONL to the sync branch using a worktree.
// Returns true if changes were committed, false if no changes or sync.branch not configured.
// If forceOverwrite is true, the local JSONL is copied to the worktree without merging,
// which is necessary for delete mutations to be properly reflected in the sync branch.
func syncBranchCommitAndPushWithOptions(ctx context.Context, store storage.Storage, autoPush, forceOverwrite bool, log daemonLogger) (bool, error) {
	// Check if any remote exists (bd-biwp: support local-only repos)
	if !hasGitRemote(ctx) {
		return true, nil // Skip sync branch commit/push in local-only mode
	}

	// Guard: Skip if sync-branch == current-branch (GH#1258)
	if shouldSkipDueToSameBranch(ctx, store, "sync-branch commit", log) {
		return false, nil
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

	// Skip JSONL sync branch operations in dolt-native mode (bd-ats9.3.1)
	// Dolt handles sync through its own Push/Pull methods, not JSONL
	if !ShouldExportJSONL(ctx, store) {
		return true, nil // Signal "handled" to prevent fallback to regular git commit
	}

	log.log("Using sync branch: %s", syncBranch)
	
	// Get main repo root (for worktrees, this is the main repo, not worktree)
	repoRoot, err := git.GetMainRepoRoot()
	if err != nil {
		return false, fmt.Errorf("failed to get main repo root: %w", err)
	}
	
	// Use worktree-aware git directory detection
	gitDir, err := git.GetGitDir()
	if err != nil {
		return false, fmt.Errorf("not a git repository: %w", err)
	}
	
	// Worktree path is under .git/beads-worktrees/<branch>
	worktreePath := filepath.Join(gitDir, "beads-worktrees", syncBranch)
	
	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)
	
	// Ensure worktree exists and is healthy
	// CreateBeadsWorktree now performs a full health check internally and
	// automatically repairs unhealthy worktrees by removing and recreating them
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return false, fmt.Errorf("failed to create worktree: %w", err)
	}
	
	// Sync JSONL file to worktree
	// Get the actual JSONL path
	jsonlPath := findJSONLPath()
	if jsonlPath == "" {
		return false, fmt.Errorf("beads storage file not found")
	}
	
	// Convert absolute path to relative path from repo root
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return false, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}
	
	// Use SyncJSONLToWorktreeWithOptions to pass forceOverwrite flag.
	// When forceOverwrite is true (mutation-triggered sync, especially delete),
	// the local JSONL is copied directly without merging, ensuring deletions
	// are properly reflected in the sync branch.
	syncOpts := git.SyncOptions{ForceOverwrite: forceOverwrite}
	if err := wtMgr.SyncJSONLToWorktreeWithOptions(worktreePath, jsonlRelPath, syncOpts); err != nil {
		return false, fmt.Errorf("failed to sync JSONL to worktree: %w", err)
	}
	
	// Check for changes in worktree
	// GH#810: Normalize path for bare repo worktrees
	normalizedRelPath := git.NormalizeBeadsRelPath(jsonlRelPath)
	worktreeJSONLPath := filepath.Join(worktreePath, normalizedRelPath)
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
		// Get configured remote from bd config (sync.remote), default to empty (will use git config)
		configuredRemote, _ := store.GetConfig(ctx, "sync.remote")
		if err := gitPushFromWorktree(ctx, worktreePath, syncBranch, configuredRemote); err != nil {
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
	// Use --sparse to work correctly with sparse-checkout enabled worktrees (fixes #1076)
	addCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", "--sparse", relPath) // #nosec G204 - worktreePath and relPath are derived from trusted git operations
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed in worktree: %w", err)
	}

	// Build commit args with config-based author and signing options (GH#1051)
	// Also use --no-verify to skip hooks (pre-commit hook would fail in worktree context)
	// The worktree is internal to bd sync, so we don't need to run bd's pre-commit hook
	args := []string{"-C", worktreePath, "commit", "--no-verify"}

	// Add --author if configured (GH#1051: apply git.author config to daemon commits)
	if author := config.GetString("git.author"); author != "" {
		args = append(args, "--author", author)
	}

	// Add --no-gpg-sign if configured
	if config.GetBool("git.no-gpg-sign") {
		args = append(args, "--no-gpg-sign")
	}

	args = append(args, "-m", message)

	commitCmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 - args built from trusted config values
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed in worktree: %w\n%s", err, output)
	}

	return nil
}

// gitPushFromWorktree pushes the sync branch from the worktree.
// If push fails due to remote having newer commits, it will fetch, rebase, and retry.
// The configuredRemote parameter allows passing the bd config sync.remote value.
func gitPushFromWorktree(ctx context.Context, worktreePath, branch, configuredRemote string) error {
	// Use configured remote if provided, otherwise check git branch config
	remote := configuredRemote
	if remote == "" {
		remoteCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) // #nosec G204 - worktreePath and branch are from config
		remoteOutput, err := remoteCmd.Output()
		if err != nil {
			// If no remote configured, default to "origin" and set up tracking
			remoteOutput = []byte("origin\n")
		}
		remote = strings.TrimSpace(string(remoteOutput))
	}
	
	// Push with explicit remote and branch, set upstream if not set
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "--set-upstream", remote, branch) // #nosec G204 - worktreePath, remote, and branch are from config
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if push failed due to remote having newer commits
		outputStr := string(output)
		if strings.Contains(outputStr, "fetch first") || strings.Contains(outputStr, "non-fast-forward") {
			// Fetch and rebase, then retry push
			fetchCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "fetch", remote, branch) // #nosec G204
			if fetchOutput, fetchErr := fetchCmd.CombinedOutput(); fetchErr != nil {
				return fmt.Errorf("git fetch failed in worktree: %w\n%s", fetchErr, fetchOutput)
			}
			
			// Rebase local commits on top of remote
			rebaseCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "rebase", remote+"/"+branch) // #nosec G204
			if rebaseOutput, rebaseErr := rebaseCmd.CombinedOutput(); rebaseErr != nil {
				// If rebase fails (conflict), abort and return error
				abortCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "rebase", "--abort") // #nosec G204
				_ = abortCmd.Run()
				return fmt.Errorf("git rebase failed in worktree (sync branch may have conflicts): %w\n%s", rebaseErr, rebaseOutput)
			}
			
			// Retry push after successful rebase
			retryCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "--set-upstream", remote, branch) // #nosec G204
			if retryOutput, retryErr := retryCmd.CombinedOutput(); retryErr != nil {
				return fmt.Errorf("git push failed after rebase: %w\n%s", retryErr, retryOutput)
			}
			
			return nil
		}
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

	// Guard: Skip if sync-branch == current-branch (GH#1258)
	if shouldSkipDueToSameBranch(ctx, store, "sync-branch pull", log) {
		return false, nil
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

	// Skip JSONL sync branch operations in dolt-native mode (bd-ats9.3.1)
	// Dolt handles sync through its own Push/Pull methods, not JSONL
	if !ShouldExportJSONL(ctx, store) {
		return true, nil // Signal "handled" to prevent fallback to regular git pull
	}

	// Get main repo root (for worktrees, this is the main repo, not worktree)
	repoRoot, err := git.GetMainRepoRoot()
	if err != nil {
		return false, fmt.Errorf("failed to get main repo root: %w", err)
	}
	
	// Use worktree-aware git directory detection
	gitDir, err := git.GetGitDir()
	if err != nil {
		return false, fmt.Errorf("not a git repository: %w", err)
	}
	
	// Worktree path is under .git/beads-worktrees/<branch>
	worktreePath := filepath.Join(gitDir, "beads-worktrees", syncBranch)
	
	// Initialize worktree manager
	wtMgr := git.NewWorktreeManager(repoRoot)
	
	// Ensure worktree exists
	if err := wtMgr.CreateBeadsWorktree(syncBranch, worktreePath); err != nil {
		return false, fmt.Errorf("failed to create worktree: %w", err)
	}
	
	// Get remote name - check bd config first, then git branch config, then default to "origin"
	remote := ""
	if configuredRemote, err := store.GetConfig(ctx, "sync.remote"); err == nil && configuredRemote != "" {
		remote = configuredRemote
	} else {
		remoteCmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "config", "--get", fmt.Sprintf("branch.%s.remote", syncBranch)) // #nosec G204 - worktreePath and syncBranch are from config
		remoteOutput, err := remoteCmd.Output()
		if err != nil {
			// If no remote configured, default to "origin"
			remoteOutput = []byte("origin\n")
		}
		remote = strings.TrimSpace(string(remoteOutput))
	}
	
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
		return false, fmt.Errorf("beads storage file not found")
	}
	
	// Convert to relative path
	jsonlRelPath, err := filepath.Rel(repoRoot, jsonlPath)
	if err != nil {
		return false, fmt.Errorf("failed to get relative JSONL path: %w", err)
	}
	
	// Copy JSONL back to main repo
	// GH#810: Normalize path for bare repo worktrees
	normalizedRelPath := git.NormalizeBeadsRelPath(jsonlRelPath)
	worktreeJSONLPath := filepath.Join(worktreePath, normalizedRelPath)
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
