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

// isGitRepo checks if the current working directory is in a git repository.
// NOTE: This intentionally checks CWD, not the beads repo. It's used as a guard
// before calling other git functions to prevent hangs on Windows (GH#727).
// Does not use RepoContext because it's a prerequisite check for git availability.
func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// gitHasUnmergedPaths checks for unmerged paths or merge in progress in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository.
func gitHasUnmergedPaths() (bool, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false, fmt.Errorf("getting repo context: %w", err)
	}

	ctx := context.Background()
	cmd := rc.GitCmd(ctx, "status", "--porcelain")
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
	mergeCmd := rc.GitCmd(ctx, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if mergeCmd.Run() == nil {
		return true, nil
	}

	return false, nil
}

// gitHasUpstream checks if the current branch has an upstream configured in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository.
// Uses git config directly for compatibility with Git for Windows.
func gitHasUpstream() bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}

	ctx := context.Background()
	// Get current branch name
	branchCmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return false
	}
	branch := strings.TrimSpace(string(branchOutput))

	return gitBranchHasUpstream(branch)
}

// gitBranchHasUpstream checks if a specific branch has an upstream configured.
// Unlike gitHasUpstream(), this works even when HEAD is detached (e.g., jj/jujutsu).
// This is critical for sync-branch workflows where the sync branch has upstream
// tracking but the main working copy may be in detached HEAD state.
// Uses RepoContext to ensure git commands run in the correct repository.
func gitBranchHasUpstream(branch string) bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}

	ctx := context.Background()
	// Check if remote and merge refs are configured for the branch
	remoteCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) //nolint:gosec // G204: branch from caller
	mergeCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.merge", branch))   //nolint:gosec // G204: branch from caller

	remoteErr := remoteCmd.Run()
	mergeErr := mergeCmd.Run()

	return remoteErr == nil && mergeErr == nil
}

// gitHasChanges checks if the specified file has uncommitted changes in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository.
func gitHasChanges(ctx context.Context, filePath string) (bool, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false, fmt.Errorf("getting repo context: %w", err)
	}

	cmd := rc.GitCmd(ctx, "status", "--porcelain", filePath)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// gitHasBeadsChanges checks if any tracked files in .beads/ have uncommitted changes.
// Uses RepoContext to ensure git commands run in the correct repository.
// RepoContext handles worktrees (GH#827) and redirected beads directories (bd-arjb).
func gitHasBeadsChanges(ctx context.Context) (bool, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false, fmt.Errorf("getting repo context: %w", err)
	}

	// rc.GitCmd runs in rc.RepoRoot which is the repo containing .beads/
	// This works for both normal and redirected scenarios.
	cmd := rc.GitCmd(ctx, "status", "--porcelain", rc.BeadsDir)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// buildGitCommitArgs returns git commit args with config-based author and signing options (GH#600)
// This allows users to configure a separate author and disable GPG signing for beads commits.
// Includes -C repoRoot for use with raw exec.Command (not RepoContext).
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

// buildCommitArgs returns git commit args for use with RepoContext.GitCmd().
// Unlike buildGitCommitArgs, this does NOT include -C flag since GitCmd sets cmd.Dir.
// Applies config-based author and signing options (GH#600).
func buildCommitArgs(message string, extraArgs ...string) []string {
	args := []string{"commit"}

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

// gitCommit commits the specified file in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository (worktree-aware).
func gitCommit(ctx context.Context, filePath string, message string) error {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}

	// Make file path relative to repo root for git operations
	relPath, err := filepath.Rel(rc.RepoRoot, filePath)
	if err != nil {
		relPath = filePath // Fall back to absolute path
	}

	// Stage the file
	addCmd := rc.GitCmd(ctx, "add", relPath)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Generate message if not provided
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// Commit with config-based author and signing options
	// Use pathspec to commit ONLY this file
	// This prevents accidentally committing other staged files
	commitArgs := buildCommitArgs(message, "--", relPath)
	commitCmd := rc.GitCmd(ctx, commitArgs...)
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
// Uses RepoContext to ensure git commands run in the correct repository.
// Handles worktrees and redirected beads directories.
func gitCommitBeadsDir(ctx context.Context, message string) error {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}

	// Stage only the specific sync-related files
	// This avoids staging gitignored snapshot files (beads.*.jsonl, *.meta.json)
	// that may still be tracked from before they were added to .gitignore
	syncFiles := []string{
		filepath.Join(rc.BeadsDir, "issues.jsonl"),
		filepath.Join(rc.BeadsDir, "deletions.jsonl"),
		filepath.Join(rc.BeadsDir, "interactions.jsonl"),
		filepath.Join(rc.BeadsDir, "metadata.json"),
	}

	// Only add files that exist
	var filesToAdd []string
	for _, f := range syncFiles {
		if _, err := os.Stat(f); err == nil {
			// Convert to relative path from repo root for git operations
			relPath, err := filepath.Rel(rc.RepoRoot, f)
			if err != nil {
				relPath = f // Fall back to absolute path if relative fails
			}
			filesToAdd = append(filesToAdd, relPath)
		}
	}

	if len(filesToAdd) == 0 {
		return fmt.Errorf("no sync files found to commit")
	}

	// Stage only the sync files
	addArgs := append([]string{"add"}, filesToAdd...)
	addCmd := rc.GitCmd(ctx, addArgs...)
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
	relBeadsDir, err := filepath.Rel(rc.RepoRoot, rc.BeadsDir)
	if err != nil {
		relBeadsDir = rc.BeadsDir // Fall back to absolute path if relative fails
	}

	// Use config-based author and signing options with pathspec
	commitArgs := buildCommitArgs(message, "--", relBeadsDir)
	commitCmd := rc.GitCmd(ctx, commitArgs...)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	return nil
}

// hasGitRemote checks if a git remote exists in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository
// regardless of current working directory.
func hasGitRemote(ctx context.Context) bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}
	cmd := rc.GitCmd(ctx, "remote")
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

// hasJSONLConflict checks if the beads JSONL file has a merge conflict in the beads repository.
// Returns true only if the JSONL file (issues.jsonl or beads.jsonl) is the only file in conflict.
// Uses RepoContext to ensure git commands run in the correct repository.
func hasJSONLConflict() bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}

	ctx := context.Background()
	cmd := rc.GitCmd(ctx, "status", "--porcelain")
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
			filePath := strings.TrimSpace(line[3:])

			// Check for beads JSONL files (issues.jsonl or beads.jsonl in .beads/)
			if strings.HasSuffix(filePath, "issues.jsonl") || strings.HasSuffix(filePath, "beads.jsonl") {
				hasJSONLConflict = true
			} else {
				hasOtherConflict = true
			}
		}
	}

	// Only return true if ONLY the JSONL file has a conflict
	return hasJSONLConflict && !hasOtherConflict
}

// runGitRebaseContinue continues a rebase after resolving conflicts in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository.
func runGitRebaseContinue(ctx context.Context) error {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}

	cmd := rc.GitCmd(ctx, "rebase", "--continue")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git rebase --continue failed: %w\n%s", err, output)
	}
	return nil
}

// gitPull pulls from the current branch's upstream in the beads repository.
// Returns nil if no remote configured (local-only mode).
// If configuredRemote is non-empty, uses that instead of the branch's configured remote.
// This allows respecting the sync.remote bd config.
// Uses RepoContext to ensure git commands run in the correct repository.
func gitPull(ctx context.Context, configuredRemote string) error {
	// Check if any remote exists (support local-only repos)
	if !hasGitRemote(ctx) {
		return nil // Gracefully skip - local-only mode
	}

	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}

	// Get current branch name
	// Use symbolic-ref to work in fresh repos without commits
	branchCmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
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
		remoteCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) //nolint:gosec // G204: branch from git symbolic-ref
		remoteOutput, err := remoteCmd.Output()
		if err != nil {
			// If no remote configured, default to "origin"
			remote = "origin"
		} else {
			remote = strings.TrimSpace(string(remoteOutput))
		}
	}

	// Pull with explicit remote and branch
	cmd := rc.GitCmd(ctx, "pull", remote, branch) //nolint:gosec // G204: remote/branch from git config, not user input
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}
	return nil
}

// gitPush pushes to the current branch's upstream in the beads repository.
// Returns nil if no remote configured (local-only mode).
// If configuredRemote is non-empty, pushes to that remote explicitly.
// This allows respecting the sync.remote bd config.
// Uses RepoContext to ensure git commands run in the correct repository.
func gitPush(ctx context.Context, configuredRemote string) error {
	// Check if any remote exists (support local-only repos)
	if !hasGitRemote(ctx) {
		return nil // Gracefully skip - local-only mode
	}

	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}

	// If configuredRemote is set, push explicitly to that remote with current branch
	if configuredRemote != "" {
		// Get current branch name
		branchCmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
		branchOutput, err := branchCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get current branch: %w", err)
		}
		branch := strings.TrimSpace(string(branchOutput))

		cmd := rc.GitCmd(ctx, "push", configuredRemote, branch) //nolint:gosec // G204: configuredRemote from bd config
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git push failed: %w\n%s", err, output)
		}
		return nil
	}

	// Default: use git's default push behavior
	cmd := rc.GitCmd(ctx, "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %w\n%s", err, output)
	}
	return nil
}

// checkMergeDriverConfig checks if the merge driver is misconfigured in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository.
func checkMergeDriverConfig() {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return // No beads context, skip check
	}

	ctx := context.Background()
	// Get current merge driver configuration
	cmd := rc.GitCmd(ctx, "config", "merge.beads.driver")
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

// gitHasUncommittedBeadsChanges checks if .beads/issues.jsonl has uncommitted changes.
// This detects the failure mode where a previous sync exported but failed before commit.
// Returns true if the JSONL file has staged or unstaged changes (M or A status).
// GH#885: Pre-flight safety check to detect incomplete sync operations.
// Uses RepoContext to ensure git commands run in the correct repository (handles redirects).
func gitHasUncommittedBeadsChanges(ctx context.Context) (bool, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false, nil // No beads context, nothing to check
	}

	jsonlPath := filepath.Join(rc.BeadsDir, "issues.jsonl")

	// rc.GitCmd runs in rc.RepoRoot which is the repo containing .beads/
	// This works for both normal and redirected scenarios.
	cmd := rc.GitCmd(ctx, "status", "--porcelain", jsonlPath)
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

// getDefaultBranch returns the default branch name (main or master) for origin remote.
// Uses RepoContext to ensure git commands run in the correct repository.
// Checks remote HEAD first, then falls back to checking if main/master exist.
func getDefaultBranch(ctx context.Context) string {
	return getDefaultBranchForRemote(ctx, "origin")
}

// getDefaultBranchForRemote returns the default branch name for a specific remote in the beads repository.
// Uses RepoContext to ensure git commands run in the correct repository.
// Checks remote HEAD first, then falls back to checking if main/master exist.
func getDefaultBranchForRemote(ctx context.Context, remote string) string {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return "main" // Default fallback if context unavailable
	}

	// Try to get default branch from remote
	cmd := rc.GitCmd(ctx, "symbolic-ref", fmt.Sprintf("refs/remotes/%s/HEAD", remote)) //nolint:gosec // G204: remote from git config
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
	mainCmd := rc.GitCmd(ctx, "rev-parse", "--verify", fmt.Sprintf("%s/main", remote)) //nolint:gosec // G204: remote from git config
	if mainCmd.Run() == nil {
		return "main"
	}

	// Fallback: check if <remote>/master exists
	masterCmd := rc.GitCmd(ctx, "rev-parse", "--verify", fmt.Sprintf("%s/master", remote)) //nolint:gosec // G204: remote from git config
	if masterCmd.Run() == nil {
		return "master"
	}

	// Default to main
	return "main"
}
