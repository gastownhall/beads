package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
)

// isGitRepo checks if the current directory is in a git repository
func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// gitHasUnmergedPaths checks for unmerged paths or merge in progress
func gitHasUnmergedPaths() (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}

	// Check for unmerged status codes (DD, AU, UD, UA, DU, AA, UU)
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) >= 2 {
			s := line[:2]
			if s == "DD" || s == "AU" || s == "UD" || s == "UA" || s == "DU" || s == "AA" || s == "UU" {
				return true, nil
			}
		}
	}

	// Check if MERGE_HEAD exists (merge in progress)
	if exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD").Run() == nil {
		return true, nil
	}

	return false, nil
}

// gitHasUpstream checks if the current branch has an upstream configured
// Uses git config directly for compatibility with Git for Windows
func gitHasUpstream() bool {
	// Get current branch name
	branchCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return false
	}
	branch := strings.TrimSpace(string(branchOutput))

	// Check if remote and merge refs are configured
	remoteCmd := exec.Command("git", "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) //nolint:gosec // G204: branch from git symbolic-ref
	mergeCmd := exec.Command("git", "config", "--get", fmt.Sprintf("branch.%s.merge", branch))   //nolint:gosec // G204: branch from git symbolic-ref

	remoteErr := remoteCmd.Run()
	mergeErr := mergeCmd.Run()

	return remoteErr == nil && mergeErr == nil
}

// gitHasChanges checks if the specified file has uncommitted changes
func gitHasChanges(ctx context.Context, filePath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", filePath)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// getRepoRootForWorktree returns the main repository root for running git commands
// This is always the main repository root, never the worktree root
func getRepoRootForWorktree(_ context.Context) string {
	repoRoot, err := git.GetMainRepoRoot()
	if err != nil {
		// Fallback to current directory if GetMainRepoRoot fails
		return "."
	}
	return repoRoot
}

// gitHasBeadsChanges checks if any tracked files in .beads/ have uncommitted changes
// This function is worktree-aware and handles bare repo worktree setups (GH#827).
// It also handles redirected beads directories (bd-arjb) by running git commands
// from the directory containing the actual .beads/.
func gitHasBeadsChanges(ctx context.Context) (bool, error) {
	// Get the absolute path to .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return false, fmt.Errorf("no .beads directory found")
	}

	// Check if beads directory is redirected (bd-arjb)
	// When redirected, beadsDir points outside the current repo, so we need to
	// run git commands from the directory containing the actual .beads/
	redirectInfo := beads.GetRedirectInfo()
	if redirectInfo.IsRedirected {
		// beadsDir is the target (e.g., /path/to/mayor/rig/.beads)
		// We need to run git from the parent of .beads (e.g., /path/to/mayor/rig)
		targetRepoDir := filepath.Dir(beadsDir)
		statusCmd := exec.CommandContext(ctx, "git", "-C", targetRepoDir, "status", "--porcelain", beadsDir) //nolint:gosec // G204: beadsDir from beads.FindBeadsDir()
		statusOutput, err := statusCmd.Output()
		if err != nil {
			return false, fmt.Errorf("git status failed: %w", err)
		}
		return len(strings.TrimSpace(string(statusOutput))) > 0, nil
	}

	// Run git status with absolute path from current directory.
	// This is more robust than using -C with a repo root, because:
	// 1. In bare repo worktree setups, GetMainRepoRoot() returns the parent
	//    of the bare repo, which isn't a valid working tree (GH#827)
	// 2. Git will find the repository from cwd, which is always valid
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain", beadsDir) //nolint:gosec // G204: beadsDir from beads.FindBeadsDir()
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(statusOutput))) > 0, nil
}

// buildGitCommitArgs returns git commit args with config-based author and signing options (GH#600)
// This allows users to configure a separate author and disable GPG signing for beads commits.
func buildGitCommitArgs(repoRoot, message string, extraArgs ...string) []string {
	args := []string{"-C", repoRoot, "commit"}

	// Add --author if configured
	if author := config.GetString("git.author"); author != "" {
		args = append(args, "--author", author)
	}

	// Add --no-gpg-sign if configured
	if config.GetBool("git.no-gpg-sign") {
		args = append(args, "--no-gpg-sign")
	}

	// Add message
	args = append(args, "-m", message)

	// Add any extra args (like -- pathspec)
	args = append(args, extraArgs...)

	return args
}

// gitCommit commits the specified file (worktree-aware)
func gitCommit(ctx context.Context, filePath string, message string) error {
	// Get the repository root (handles worktrees properly)
	repoRoot := getRepoRootForWorktree(ctx)
	if repoRoot == "" {
		return fmt.Errorf("cannot determine repository root")
	}

	// Make file path relative to repo root for git operations
	relPath, err := filepath.Rel(repoRoot, filePath)
	if err != nil {
		relPath = filePath // Fall back to absolute path
	}

	// Stage the file from repo root context
	addCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "add", relPath) //nolint:gosec // G204: paths from internal git helpers
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Generate message if not provided
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// Commit from repo root context with config-based author and signing options
	// Use pathspec to commit ONLY this file
	// This prevents accidentally committing other staged files
	commitArgs := buildGitCommitArgs(repoRoot, message, "--", relPath)
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...) //nolint:gosec // G204: args from buildGitCommitArgs
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	return nil
}

// gitCommitBeadsDir stages and commits only sync-related files in .beads/
// This ensures bd sync doesn't accidentally commit other staged files.
// Only stages specific sync files (issues.jsonl, deletions.jsonl, metadata.json)
// to avoid staging gitignored snapshot files that may be tracked.
// Worktree-aware: handles cases where .beads is in the main repo but we're running from a worktree.
func gitCommitBeadsDir(ctx context.Context, message string) error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("no .beads directory found")
	}

	// Get the repository root (handles worktrees properly)
	repoRoot := getRepoRootForWorktree(ctx)
	if repoRoot == "" {
		return fmt.Errorf("cannot determine repository root")
	}

	// Stage only the specific sync-related files
	// This avoids staging gitignored snapshot files (beads.*.jsonl, *.meta.json)
	// that may still be tracked from before they were added to .gitignore
	syncFiles := []string{
		filepath.Join(beadsDir, "issues.jsonl"),
		filepath.Join(beadsDir, "deletions.jsonl"),
		filepath.Join(beadsDir, "interactions.jsonl"),
		filepath.Join(beadsDir, "metadata.json"),
	}

	// Only add files that exist
	var filesToAdd []string
	for _, f := range syncFiles {
		if _, err := os.Stat(f); err == nil {
			// Convert to relative path from repo root for git operations
			relPath, err := filepath.Rel(repoRoot, f)
			if err != nil {
				relPath = f // Fall back to absolute path if relative fails
			}
			filesToAdd = append(filesToAdd, relPath)
		}
	}

	if len(filesToAdd) == 0 {
		return fmt.Errorf("no sync files found to commit")
	}

	// Stage only the sync files from repo root context (worktree-aware)
	args := append([]string{"-C", repoRoot, "add"}, filesToAdd...)
	addCmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: paths from internal git helpers
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Generate message if not provided
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// Commit only .beads/ files using -- pathspec
	// This prevents accidentally committing other staged files that the user
	// may have staged but wasn't ready to commit yet.
	// Convert beadsDir to relative path for git commit (worktree-aware)
	relBeadsDir, err := filepath.Rel(repoRoot, beadsDir)
	if err != nil {
		relBeadsDir = beadsDir // Fall back to absolute path if relative fails
	}

	// Use config-based author and signing options with pathspec
	commitArgs := buildGitCommitArgs(repoRoot, message, "--", relBeadsDir)
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...) //nolint:gosec // G204: args from buildGitCommitArgs
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	return nil
}

// hasGitRemote checks if a git remote exists in the repository
func hasGitRemote(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "git", "remote")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// isInRebase checks if we're currently in a git rebase state
func isInRebase() bool {
	// Get actual git directory (handles worktrees)
	gitDir, err := git.GetGitDir()
	if err != nil {
		return false
	}

	// Check for rebase-merge directory (interactive rebase)
	rebaseMergePath := filepath.Join(gitDir, "rebase-merge")
	if _, err := os.Stat(rebaseMergePath); err == nil {
		return true
	}
	// Check for rebase-apply directory (non-interactive rebase)
	rebaseApplyPath := filepath.Join(gitDir, "rebase-apply")
	if _, err := os.Stat(rebaseApplyPath); err == nil {
		return true
	}
	return false
}

// hasJSONLConflict checks if the beads JSONL file has a merge conflict
// Returns true only if the JSONL file (issues.jsonl or beads.jsonl) is the only file in conflict
func hasJSONLConflict() bool {
	cmd := exec.Command("git", "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false
	}

	var hasJSONLConflict bool
	var hasOtherConflict bool

	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 3 {
			continue
		}

		// Check for unmerged status codes (UU = both modified, AA = both added, etc.)
		status := line[:2]
		if status == "UU" || status == "AA" || status == "DD" ||
			status == "AU" || status == "UA" || status == "DU" || status == "UD" {
			filepath := strings.TrimSpace(line[3:])

			// Check for beads JSONL files (issues.jsonl or beads.jsonl in .beads/)
			if strings.HasSuffix(filepath, "issues.jsonl") || strings.HasSuffix(filepath, "beads.jsonl") {
				hasJSONLConflict = true
			} else {
				hasOtherConflict = true
			}
		}
	}

	// Only return true if ONLY the JSONL file has a conflict
	return hasJSONLConflict && !hasOtherConflict
}

// runGitRebaseContinue continues a rebase after resolving conflicts
func runGitRebaseContinue(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "rebase", "--continue")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git rebase --continue failed: %w\n%s", err, output)
	}
	return nil
}

// gitPull pulls from the current branch's upstream
// Returns nil if no remote configured (local-only mode)
// If configuredRemote is non-empty, uses that instead of the branch's configured remote.
// This allows respecting the sync.remote bd config.
func gitPull(ctx context.Context, configuredRemote string) error {
	// Check if any remote exists (support local-only repos)
	if !hasGitRemote(ctx) {
		return nil // Gracefully skip - local-only mode
	}

	// Get current branch name
	// Use symbolic-ref to work in fresh repos without commits
	branchCmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOutput))

	// Determine remote to use:
	// 1. If configuredRemote (from sync.remote bd config) is set, use that
	// 2. Otherwise, get from git branch tracking config
	// 3. Fall back to "origin"
	remote := configuredRemote
	if remote == "" {
		remoteCmd := exec.CommandContext(ctx, "git", "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) //nolint:gosec // G204: branch from git symbolic-ref
		remoteOutput, err := remoteCmd.Output()
		if err != nil {
			// If no remote configured, default to "origin"
			remote = "origin"
		} else {
			remote = strings.TrimSpace(string(remoteOutput))
		}
	}

	// Pull with explicit remote and branch
	cmd := exec.CommandContext(ctx, "git", "pull", remote, branch) //nolint:gosec // G204: remote/branch from git config, not user input
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}
	return nil
}

// gitPush pushes to the current branch's upstream
// Returns nil if no remote configured (local-only mode)
// If configuredRemote is non-empty, pushes to that remote explicitly.
// This allows respecting the sync.remote bd config.
func gitPush(ctx context.Context, configuredRemote string) error {
	// Check if any remote exists (support local-only repos)
	if !hasGitRemote(ctx) {
		return nil // Gracefully skip - local-only mode
	}

	// If configuredRemote is set, push explicitly to that remote with current branch
	if configuredRemote != "" {
		// Get current branch name
		branchCmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
		branchOutput, err := branchCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get current branch: %w", err)
		}
		branch := strings.TrimSpace(string(branchOutput))

		cmd := exec.CommandContext(ctx, "git", "push", configuredRemote, branch) //nolint:gosec // G204: configuredRemote from bd config
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git push failed: %w\n%s", err, output)
		}
		return nil
	}

	// Default: use git's default push behavior
	cmd := exec.CommandContext(ctx, "git", "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %w\n%s", err, output)
	}
	return nil
}

func checkMergeDriverConfig() {
	// Get current merge driver configuration
	cmd := exec.Command("git", "config", "merge.beads.driver")
	output, err := cmd.Output()
	if err != nil {
		// No merge driver configured - this is OK, user may not need it
		return
	}

	currentConfig := strings.TrimSpace(string(output))

	// Check if using old incorrect placeholders
	if strings.Contains(currentConfig, "%L") || strings.Contains(currentConfig, "%R") {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Git merge driver is misconfigured!\n")
		fmt.Fprintf(os.Stderr, "   Current: %s\n", currentConfig)
		fmt.Fprintf(os.Stderr, "   Problem: Git only supports %%O (base), %%A (current), %%B (other)\n")
		fmt.Fprintf(os.Stderr, "            Using %%L/%%R will cause merge failures!\n")
		fmt.Fprintf(os.Stderr, "\n   Fix now: bd doctor --fix\n")
		fmt.Fprintf(os.Stderr, "   Or manually: git config merge.beads.driver \"bd merge %%A %%O %%A %%B\"\n\n")
	}
}

// restoreBeadsDirFromBranch restores .beads/ directory from the current branch's committed state.
// This is used after sync when sync.branch is configured to keep the working directory clean.
// The actual beads data lives on the sync branch; the main branch's .beads/ is just a snapshot.
func restoreBeadsDirFromBranch(ctx context.Context) error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("no .beads directory found")
	}

	// Restore .beads/ from HEAD (current branch's committed state)
	// Using -- to ensure .beads/ is treated as a path, not a branch name
	cmd := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", beadsDir) //nolint:gosec // G204: beadsDir from FindBeadsDir(), not user input
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout failed: %w\n%s", err, output)
	}
	return nil
}

// gitHasUncommittedBeadsChanges checks if .beads/issues.jsonl has uncommitted changes.
// This detects the failure mode where a previous sync exported but failed before commit.
// Returns true if the JSONL file has staged or unstaged changes (M or A status).
// GH#885: Pre-flight safety check to detect incomplete sync operations.
// Also handles redirected beads directories (bd-arjb).
func gitHasUncommittedBeadsChanges(ctx context.Context) (bool, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return false, nil // No beads dir, nothing to check
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Check if beads directory is redirected (bd-arjb)
	// When redirected, beadsDir points outside the current repo, so we need to
	// run git commands from the directory containing the actual .beads/
	redirectInfo := beads.GetRedirectInfo()
	if redirectInfo.IsRedirected {
		targetRepoDir := filepath.Dir(beadsDir)
		cmd := exec.CommandContext(ctx, "git", "-C", targetRepoDir, "status", "--porcelain", jsonlPath) //nolint:gosec // G204: jsonlPath from internal beads.FindBeadsDir()
		output, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("git status failed: %w", err)
		}
		return parseGitStatusForBeadsChanges(string(output)), nil
	}

	// Check git status for the JSONL file specifically
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", jsonlPath) //nolint:gosec // G204: jsonlPath from internal beads.FindBeadsDir()
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}

	return parseGitStatusForBeadsChanges(string(output)), nil
}

// parseGitStatusForBeadsChanges parses git status --porcelain output and returns
// true if the status indicates uncommitted changes (modified or added).
// Format: XY filename where X=staged, Y=unstaged
// M = modified, A = added, ? = untracked, D = deleted
// Only M and A in either position indicate changes we care about.
func parseGitStatusForBeadsChanges(statusOutput string) bool {
	statusLine := strings.TrimSpace(statusOutput)
	if statusLine == "" {
		return false // No changes
	}

	// Any status (M, A, MM, AM, etc.) indicates uncommitted changes
	if len(statusLine) >= 2 {
		x, y := statusLine[0], statusLine[1]
		// Check for modifications (staged or unstaged)
		if x == 'M' || x == 'A' || y == 'M' || y == 'A' {
			return true
		}
	}

	return false
}

// rollbackJSONLFromGit restores the JSONL file from git HEAD after a failed commit.
// This is part of the sync atomicity fix (GH#885/bd-3bhl): when git commit fails
// after export, we restore the JSONL to its previous state so the working
// directory stays consistent with the last successful sync.
func rollbackJSONLFromGit(ctx context.Context, jsonlPath string) error {
	// Check if the file is tracked by git
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", jsonlPath)
	if err := cmd.Run(); err != nil {
		// File not tracked - nothing to restore
		return nil
	}

	// Restore from HEAD
	restoreCmd := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", jsonlPath) //nolint:gosec // G204: jsonlPath from internal beads.FindBeadsDir()
	output, err := restoreCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout failed: %w\n%s", err, output)
	}
	return nil
}

// getDefaultBranch returns the default branch name (main or master) for origin remote
// Checks remote HEAD first, then falls back to checking if main/master exist
func getDefaultBranch(ctx context.Context) string {
	return getDefaultBranchForRemote(ctx, "origin")
}

// getDefaultBranchForRemote returns the default branch name for a specific remote
// Checks remote HEAD first, then falls back to checking if main/master exist
func getDefaultBranchForRemote(ctx context.Context, remote string) string {
	// Try to get default branch from remote
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", fmt.Sprintf("refs/remotes/%s/HEAD", remote)) //nolint:gosec // G204: remote from git config
	output, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(output))
		// Extract branch name from refs/remotes/<remote>/main
		prefix := fmt.Sprintf("refs/remotes/%s/", remote)
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}

	// Fallback: check if <remote>/main exists
	if exec.CommandContext(ctx, "git", "rev-parse", "--verify", fmt.Sprintf("%s/main", remote)).Run() == nil { //nolint:gosec // G204: remote from git config
		return "main"
	}

	// Fallback: check if <remote>/master exists
	if exec.CommandContext(ctx, "git", "rev-parse", "--verify", fmt.Sprintf("%s/master", remote)).Run() == nil { //nolint:gosec // G204: remote from git config
		return "master"
	}

	// Default to main
	return "main"
}
