package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/syncbranch"
)

// getCurrentBranch returns the name of the current git branch
// Uses symbolic-ref instead of rev-parse to work in fresh repos without commits (bd-flil)
func getCurrentBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// getSyncBranch returns the configured sync branch name
func getSyncBranch(ctx context.Context) (string, error) {
	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		return "", fmt.Errorf("failed to initialize store: %w", err)
	}

	syncBranch, err := syncbranch.Get(ctx, store)
	if err != nil {
		return "", fmt.Errorf("failed to get sync branch config: %w", err)
	}

	if syncBranch == "" {
		return "", fmt.Errorf("sync.branch not configured (run 'bd config set sync.branch <branch-name>')")
	}

	return syncBranch, nil
}

// showSyncStatus shows the diff between sync branch and main branch
func showSyncStatus(ctx context.Context) error {
	if !isGitRepo() {
		return fmt.Errorf("not in a git repository")
	}

	currentBranch, err := getCurrentBranch(ctx)
	if err != nil {
		return err
	}

	syncBranch, err := getSyncBranch(ctx)
	if err != nil {
		return err
	}

	// Check if sync branch exists
	checkCmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+syncBranch) //nolint:gosec // syncBranch from config
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("sync branch '%s' does not exist", syncBranch)
	}

	fmt.Printf("Current branch: %s\n", currentBranch)
	fmt.Printf("Sync branch: %s\n\n", syncBranch)

	// Show commit diff
	fmt.Println("Commits in sync branch not in main:")
	logCmd := exec.CommandContext(ctx, "git", "log", "--oneline", currentBranch+".."+syncBranch) //nolint:gosec // branch names from git
	logOutput, err := logCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get commit log: %w\n%s", err, logOutput)
	}

	if len(strings.TrimSpace(string(logOutput))) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Print(string(logOutput))
	}

	fmt.Println("\nCommits in main not in sync branch:")
	logCmd = exec.CommandContext(ctx, "git", "log", "--oneline", syncBranch+".."+currentBranch) //nolint:gosec // branch names from git
	logOutput, err = logCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get commit log: %w\n%s", err, logOutput)
	}

	if len(strings.TrimSpace(string(logOutput))) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Print(string(logOutput))
	}

	// Show file diff for .beads/issues.jsonl
	fmt.Println("\nFile differences in .beads/issues.jsonl:")
	diffCmd := exec.CommandContext(ctx, "git", "diff", currentBranch+"..."+syncBranch, "--", ".beads/issues.jsonl") //nolint:gosec // branch names from git
	diffOutput, err := diffCmd.CombinedOutput()
	if err != nil {
		// diff returns non-zero when there are differences, which is fine
		if len(diffOutput) == 0 {
			return fmt.Errorf("failed to get diff: %w", err)
		}
	}

	if len(strings.TrimSpace(string(diffOutput))) == 0 {
		fmt.Println("  (no differences)")
	} else {
		fmt.Print(string(diffOutput))
	}

	return nil
}

// mergeSyncBranch merges the sync branch back to the main branch
func mergeSyncBranch(ctx context.Context, dryRun bool) error {
	if !isGitRepo() {
		return fmt.Errorf("not in a git repository")
	}

	currentBranch, err := getCurrentBranch(ctx)
	if err != nil {
		return err
	}

	syncBranch, err := getSyncBranch(ctx)
	if err != nil {
		return err
	}

	// Check if sync branch exists
	checkCmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+syncBranch) //nolint:gosec // syncBranch from config
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("sync branch '%s' does not exist", syncBranch)
	}

	// Check if there are uncommitted changes
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		return fmt.Errorf("uncommitted changes detected - commit or stash them first")
	}

	fmt.Printf("Merging sync branch '%s' into '%s'...\n", syncBranch, currentBranch)

	if dryRun {
		fmt.Println("→ [DRY RUN] Would merge sync branch")
		// Show what would be merged
		logCmd := exec.CommandContext(ctx, "git", "log", "--oneline", currentBranch+".."+syncBranch) //nolint:gosec // branch names from git
		logOutput, _ := logCmd.CombinedOutput()
		if len(strings.TrimSpace(string(logOutput))) > 0 {
			fmt.Println("\nCommits that would be merged:")
			fmt.Print(string(logOutput))
		} else {
			fmt.Println("No commits to merge")
		}
		return nil
	}

	// Perform the merge
	mergeCmd := exec.CommandContext(ctx, "git", "merge", syncBranch, "-m", fmt.Sprintf("Merge sync branch '%s'", syncBranch)) //nolint:gosec // syncBranch from config
	mergeOutput, err := mergeCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge failed: %w\n%s", err, mergeOutput)
	}

	fmt.Print(string(mergeOutput))
	fmt.Println("\n✓ Merge complete")

	// Suggest next steps
	fmt.Println("\nNext steps:")
	fmt.Println("1. Review the merged changes")
	fmt.Println("2. Run 'bd sync --import-only' to sync the database with merged JSONL")
	fmt.Println("3. Run 'bd sync' to push changes to remote")

	return nil
}

// isExternalBeadsDir checks if the beads directory is in a different git repo than cwd.
// This is used to detect when BEADS_DIR points to a separate repository.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
//
// GH#810: Use git-common-dir for comparison instead of repo root.
// For bare repo worktrees, GetRepoRoot returns incorrect values, causing
// local worktrees to be incorrectly identified as "external". Using
// git-common-dir correctly identifies that worktrees of the same repo
// share the same git directory.
func isExternalBeadsDir(ctx context.Context, beadsDir string) bool {
	// Get git common dir of cwd
	cwdCommonDir, err := getGitCommonDir(ctx, ".")
	if err != nil {
		return false // Can't determine, assume local
	}

	// Get git common dir of beads dir
	beadsCommonDir, err := getGitCommonDir(ctx, beadsDir)
	if err != nil {
		return false // Can't determine, assume local
	}

	return cwdCommonDir != beadsCommonDir
}

// getGitCommonDir returns the shared git directory for a path.
// For regular repos, this is the .git directory.
// For worktrees, this returns the shared git directory (common to all worktrees).
// This is the correct way to determine if two paths are in the same git repo,
// especially for bare repos and worktrees.
// GH#810: Added to fix bare repo worktree detection.
func getGitCommonDir(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git common dir for %s: %w", path, err)
	}
	result := strings.TrimSpace(string(output))
	// Make absolute for reliable comparison
	if !filepath.IsAbs(result) {
		// Resolve the input path to absolute first
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("failed to get absolute path for %s: %w", path, err)
		}
		result = filepath.Join(absPath, result)
	}
	result = filepath.Clean(result)
	// Resolve symlinks for consistent comparison (macOS /var -> /private/var)
	if resolved, err := filepath.EvalSymlinks(result); err == nil {
		result = resolved
	}
	return result, nil
}

// getRepoRootFromPath returns the git repository root for a given path.
// Unlike syncbranch.GetRepoRoot which uses cwd, this allows getting the repo root
// for any path.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
func getRepoRootFromPath(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git root for %s: %w", path, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// commitToExternalBeadsRepo commits changes directly to an external beads repo.
// Used when BEADS_DIR points to a different git repository than cwd.
// This bypasses the worktree-based sync which fails when beads dir is external.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
func commitToExternalBeadsRepo(ctx context.Context, beadsDir, message string, push bool) (bool, error) {
	repoRoot, err := getRepoRootFromPath(ctx, beadsDir)
	if err != nil {
		return false, fmt.Errorf("failed to get repo root: %w", err)
	}

	// Stage beads files (use relative path from repo root)
	relBeadsDir, err := filepath.Rel(repoRoot, beadsDir)
	if err != nil {
		relBeadsDir = beadsDir // Fallback to absolute path
	}

	addCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "add", relBeadsDir) //nolint:gosec // paths from trusted sources
	if output, err := addCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git add failed: %w\n%s", err, output)
	}

	// Check if there are staged changes
	diffCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "diff", "--cached", "--quiet") //nolint:gosec // repoRoot from git
	if diffCmd.Run() == nil {
		return false, nil // No changes to commit
	}

	// Commit with config-based author and signing options
	// Use pathspec to commit ONLY beads files (bd-trgb fix)
	// This prevents accidentally committing other staged files
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}
	commitArgs := buildGitCommitArgs(repoRoot, message, "--", relBeadsDir)
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...) //nolint:gosec // args from buildGitCommitArgs
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	// Push if requested
	if push {
		pushCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "push") //nolint:gosec // repoRoot from git
		if pushOutput, err := runGitCmdWithTimeoutMsg(ctx, pushCmd, "git push", 5*time.Second); err != nil {
			return true, fmt.Errorf("git push failed: %w\n%s", err, pushOutput)
		}
	}

	return true, nil
}

// pullFromExternalBeadsRepo pulls changes in an external beads repo.
// Used when BEADS_DIR points to a different git repository than cwd.
// Contributed by dand-oss (https://github.com/steveyegge/beads/pull/533)
func pullFromExternalBeadsRepo(ctx context.Context, beadsDir string) error {
	repoRoot, err := getRepoRootFromPath(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to get repo root: %w", err)
	}

	// Check if remote exists
	remoteCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "remote") //nolint:gosec // repoRoot from git
	remoteOutput, err := remoteCmd.Output()
	if err != nil || len(strings.TrimSpace(string(remoteOutput))) == 0 {
		return nil // No remote, skip pull
	}

	pullCmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "pull") //nolint:gosec // repoRoot from git
	if output, err := pullCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}

	return nil
}
