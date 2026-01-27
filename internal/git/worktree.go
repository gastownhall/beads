package git

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/merge"
	"github.com/steveyegge/beads/internal/utils"
)

// WorktreeManager handles git worktree lifecycle for separate beads branches
type WorktreeManager struct {
	repoPath string // Path to the main repository
}

// NewWorktreeManager creates a new worktree manager for the given repository
func NewWorktreeManager(repoPath string) *WorktreeManager {
	return &WorktreeManager{
		repoPath: repoPath,
	}
}

// CreateBeadsWorktree creates a git worktree for the beads branch with sparse checkout
// Returns the path to the created worktree
func (wm *WorktreeManager) CreateBeadsWorktree(branch, worktreePath string) error {
	// Prune stale worktree entries first
	pruneCmd := exec.Command("git", "worktree", "prune")
	pruneCmd.Dir = wm.repoPath
	_ = pruneCmd.Run() // Best effort, ignore errors
	
	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		// Worktree path exists, check if it's a valid worktree
		if valid, err := wm.isValidWorktree(worktreePath); err == nil && valid {
			// Worktree exists and is in git worktree list, verify full health
			if err := wm.CheckWorktreeHealth(worktreePath); err == nil {
				return nil // Already exists and is fully healthy
			}
			// Health check failed, try to repair by removing and recreating
			if err := wm.RemoveBeadsWorktree(worktreePath); err != nil {
				// Log but continue - we'll try to recreate anyway
				_ = os.RemoveAll(worktreePath)
			}
		} else {
			// Path exists but isn't a valid worktree, remove it
			if err := os.RemoveAll(worktreePath); err != nil {
				return fmt.Errorf("failed to remove invalid worktree path: %w", err)
			}
		}
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0750); err != nil {
		return fmt.Errorf("failed to create worktree parent directory: %w", err)
	}

	// Check if branch exists remotely or locally
	branchExists := wm.branchExists(branch)

	// Create worktree without checking out files initially
	// Use -f (force) to handle "missing but already registered" state (issue #609)
	// This occurs when the worktree directory was deleted but git registration persists
	var cmd *exec.Cmd
	if branchExists {
		// Checkout existing branch
		cmd = exec.Command("git", "worktree", "add", "-f", "--no-checkout", worktreePath, branch)
	} else {
		// Create new branch
		cmd = exec.Command("git", "worktree", "add", "-f", "--no-checkout", "-b", branch, worktreePath)
	}
	cmd.Dir = wm.repoPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create worktree: %w\nOutput: %s", err, string(output))
	}

	// Configure sparse checkout to only include .beads/
	if err := wm.configureSparseCheckout(worktreePath); err != nil {
		// Cleanup worktree on failure
		_ = wm.RemoveBeadsWorktree(worktreePath)
		return fmt.Errorf("failed to configure sparse checkout: %w", err)
	}
	
	// Now checkout the branch with sparse checkout active
	checkoutCmd := exec.Command("git", "checkout", branch)
	checkoutCmd.Dir = worktreePath
	output, err = checkoutCmd.CombinedOutput()
	if err != nil {
		_ = wm.RemoveBeadsWorktree(worktreePath)
		return fmt.Errorf("failed to checkout branch in worktree: %w\nOutput: %s", err, string(output))
	}

	// GH#886: Git 2.38+ enables sparse checkout on the main repo as a side effect
	// of worktree creation. Explicitly disable it to prevent confusing git status
	// message: "You are in a sparse checkout with 100% of tracked files present."
	disableSparseCmd := exec.Command("git", "config", "core.sparseCheckout", "false")
	disableSparseCmd.Dir = wm.repoPath
	_ = disableSparseCmd.Run() // Best effort - don't fail if this doesn't work

	return nil
}

// RemoveBeadsWorktree removes a git worktree and cleans up
func (wm *WorktreeManager) RemoveBeadsWorktree(worktreePath string) error {
	// First, try to remove via git worktree remove
	cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
	cmd.Dir = wm.repoPath
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If git worktree remove fails, manually remove the directory
		// and prune the worktree list
		if removeErr := os.RemoveAll(worktreePath); removeErr != nil {
			return fmt.Errorf("failed to remove worktree directory: %w (git error: %v, output: %s)", 
				removeErr, err, string(output))
		}
		
		// Prune stale worktree entries
		pruneCmd := exec.Command("git", "worktree", "prune")
		pruneCmd.Dir = wm.repoPath
		_ = pruneCmd.Run() // Best effort, ignore errors
	}

	return nil
}

// CheckWorktreeHealth verifies the worktree is in a good state and attempts to repair if needed
func (wm *WorktreeManager) CheckWorktreeHealth(worktreePath string) error {
	// Check if path exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return fmt.Errorf("worktree path does not exist: %s", worktreePath)
	}

	// Check if it's a valid worktree
	valid, err := wm.isValidWorktree(worktreePath)
	if err != nil {
		return fmt.Errorf("failed to check worktree validity: %w", err)
	}
	if !valid {
		return fmt.Errorf("path exists but is not a valid git worktree: %s", worktreePath)
	}

	// Check if .git file exists and points to the right place
	gitFile := filepath.Join(worktreePath, ".git")
	if _, err := os.Stat(gitFile); err != nil {
		return fmt.Errorf("worktree .git file missing: %w", err)
	}

	// Verify sparse checkout is configured correctly
	if err := wm.verifySparseCheckout(worktreePath); err != nil {
		// Try to fix by reconfiguring
		if fixErr := wm.configureSparseCheckout(worktreePath); fixErr != nil {
			return fmt.Errorf("sparse checkout invalid and failed to fix: %w (original error: %v)", fixErr, err)
		}
	}

	return nil
}

// SyncOptions configures the behavior of SyncJSONLToWorktree
type SyncOptions struct {
	// ForceOverwrite bypasses the merge logic and always overwrites the worktree
	// JSONL with the local JSONL. This should be set to true when the daemon
	// knows that a mutation (especially delete) occurred, so the local state
	// is authoritative and should not be merged with potentially stale worktree data.
	ForceOverwrite bool
}

// SyncJSONLToWorktree syncs the JSONL file from main repo to worktree.
// If the worktree has issues that the local repo doesn't have, it merges them
// instead of overwriting. This prevents data loss when a fresh clone syncs
// with fewer issues than the remote. (bd-52q fix for GitHub #464)
// Note: This is a convenience wrapper that calls SyncJSONLToWorktreeWithOptions
// with default options (ForceOverwrite=false).
func (wm *WorktreeManager) SyncJSONLToWorktree(worktreePath, jsonlRelPath string) error {
	return wm.SyncJSONLToWorktreeWithOptions(worktreePath, jsonlRelPath, SyncOptions{})
}

// SyncJSONLToWorktreeWithOptions syncs the JSONL file from main repo to worktree
// with configurable options.
// If ForceOverwrite is true, the local JSONL is always copied to the worktree,
// bypassing the merge logic. This is used when the daemon knows a mutation
// (especially delete) occurred and the local state is authoritative.
// If ForceOverwrite is false (default), the function uses merge logic to prevent
// data loss when a fresh clone syncs with fewer issues than the remote.
func (wm *WorktreeManager) SyncJSONLToWorktreeWithOptions(worktreePath, jsonlRelPath string, opts SyncOptions) error {
	// Source: main repo JSONL (use the full path as provided)
	srcPath := filepath.Join(wm.repoPath, jsonlRelPath)

	// Destination: worktree JSONL
	// GH#785, GH#810: Handle bare repo worktrees where jsonlRelPath might include the
	// worktree name (e.g., "main/.beads/issues.jsonl"). The sync branch uses
	// sparse checkout for .beads/* so we normalize to strip leading components.
	normalizedRelPath := NormalizeBeadsRelPath(jsonlRelPath)
	dstPath := filepath.Join(worktreePath, normalizedRelPath)

	// GH#1298: When sync-branch is configured, findJSONLPath() returns the worktree
	// path due to GH#1103. This causes jsonlRelPath to include the worktree path
	// (e.g., ".git/beads-worktrees/beads-sync/.beads/issues.jsonl"), making srcPath
	// point to the worktree JSONL instead of the main repo JSONL. In this case,
	// srcPath == dstPath and we're copying the file to itself - skip the sync.
	if srcPath == dstPath {
		return nil
	}

	// Ensure destination directory exists
	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0750); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Read source file
	srcData, err := os.ReadFile(srcPath) // #nosec G304 - controlled path from config
	if err != nil {
		return fmt.Errorf("failed to read source JSONL: %w", err)
	}

	// Check if destination exists and has content
	dstData, dstErr := os.ReadFile(dstPath) // #nosec G304 - controlled path
	if dstErr != nil || len(dstData) == 0 {
		// Destination doesn't exist or is empty - just copy
		if err := os.WriteFile(dstPath, srcData, 0644); err != nil { // #nosec G306 - JSONL needs to be readable
			return fmt.Errorf("failed to write destination JSONL: %w", err)
		}
		return nil
	}

	// ForceOverwrite: When the daemon knows a mutation occurred (especially delete),
	// the local state is authoritative and should overwrite the worktree without merging.
	// This fixes the bug where `bd delete` mutations were not reflected in the sync branch
	// because the merge logic would re-add the deleted issue.
	if opts.ForceOverwrite {
		if err := os.WriteFile(dstPath, srcData, 0644); err != nil { // #nosec G306 - JSONL needs to be readable
			return fmt.Errorf("failed to write destination JSONL: %w", err)
		}
		return nil
	}

	// Count issues in both files
	srcCount := countJSONLIssues(srcData)
	dstCount := countJSONLIssues(dstData)

	// If source has same or more issues, just copy (source is authoritative)
	if srcCount >= dstCount {
		if err := os.WriteFile(dstPath, srcData, 0644); err != nil { // #nosec G306 - JSONL needs to be readable
			return fmt.Errorf("failed to write destination JSONL: %w", err)
		}
		return nil
	}

	// Source has fewer issues than destination - this indicates the local repo
	// doesn't have all the issues from the sync branch. Merge instead of overwrite.
	// (bd-52q: This prevents fresh clones from accidentally deleting remote issues)
	mergedData, err := wm.mergeJSONLFiles(srcData, dstData)
	if err != nil {
		// If merge fails, fall back to copy behavior but log warning
		// This shouldn't happen but ensures we don't break existing behavior
		fmt.Fprintf(os.Stderr, "Warning: JSONL merge failed (%v), falling back to overwrite\n", err)
		if writeErr := os.WriteFile(dstPath, srcData, 0644); writeErr != nil { // #nosec G306
			return fmt.Errorf("failed to write destination JSONL: %w", writeErr)
		}
		return nil
	}

	if err := os.WriteFile(dstPath, mergedData, 0644); err != nil { // #nosec G306 - JSONL needs to be readable
		return fmt.Errorf("failed to write merged JSONL: %w", err)
	}

	return nil
}

// countJSONLIssues counts the number of valid JSON lines in JSONL data
func countJSONLIssues(data []byte) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && strings.HasPrefix(line, "{") {
			count++
		}
	}
	return count
}

// mergeJSONLFiles merges two JSONL files using 3-way merge with empty base.
// This combines issues from both files, with the source (local) taking precedence
// for issues that exist in both.
func (wm *WorktreeManager) mergeJSONLFiles(srcData, dstData []byte) ([]byte, error) {
	// Create temp files for merge
	tmpDir, err := os.MkdirTemp("", "bd-worktree-merge-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	baseFile := filepath.Join(tmpDir, "base.jsonl")
	leftFile := filepath.Join(tmpDir, "left.jsonl")   // source (local)
	rightFile := filepath.Join(tmpDir, "right.jsonl") // destination (worktree)
	outputFile := filepath.Join(tmpDir, "merged.jsonl")

	// Empty base - treat this as both sides adding issues
	if err := os.WriteFile(baseFile, []byte{}, 0600); err != nil {
		return nil, fmt.Errorf("failed to write base file: %w", err)
	}

	// Source (local) is "left" - takes precedence for conflicts
	if err := os.WriteFile(leftFile, srcData, 0600); err != nil {
		return nil, fmt.Errorf("failed to write left file: %w", err)
	}

	// Destination (worktree) is "right"
	if err := os.WriteFile(rightFile, dstData, 0600); err != nil {
		return nil, fmt.Errorf("failed to write right file: %w", err)
	}

	// Perform 3-way merge
	err = merge.Merge3Way(outputFile, baseFile, leftFile, rightFile, false)
	if err != nil {
		// Check if it's just a conflict warning (merge still produced output)
		if !strings.Contains(err.Error(), "merge completed with") {
			return nil, fmt.Errorf("3-way merge failed: %w", err)
		}
		// Conflicts are auto-resolved, continue
	}

	// Read merged result
	mergedData, err := os.ReadFile(outputFile) // #nosec G304 - temp file we created
	if err != nil {
		return nil, fmt.Errorf("failed to read merged file: %w", err)
	}

	return mergedData, nil
}

// isValidWorktree checks if the path is a valid git worktree
func (wm *WorktreeManager) isValidWorktree(worktreePath string) (bool, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = wm.repoPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to list worktrees: %w", err)
	}

	// Parse output to see if our worktree is listed
	// Use EvalSymlinks to resolve any symlinks (e.g., /tmp -> /private/tmp on macOS)
	absWorktreePath, err := filepath.EvalSymlinks(worktreePath)
	if err != nil {
		// If path doesn't exist yet, just use Abs
		absWorktreePath, err = filepath.Abs(worktreePath)
		if err != nil {
			return false, err
		}
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			// Resolve symlinks for the git-reported path too
			absPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				absPath, err = filepath.Abs(path)
				if err != nil {
					continue
				}
			}
			// Use PathsEqual to handle case-insensitive filesystems (macOS/Windows)
			if utils.PathsEqual(absPath, absWorktreePath) {
				return true, nil
			}
		}
	}

	return false, nil
}

// branchExists checks if a branch exists locally or remotely
func (wm *WorktreeManager) branchExists(branch string) bool {
	// Check local branches
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch) // #nosec G204 - branch name from config
	cmd.Dir = wm.repoPath
	if err := cmd.Run(); err == nil {
		return true
	}

	// Check remote branches
	cmd = exec.Command("git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch) // #nosec G204 - branch name from config
	cmd.Dir = wm.repoPath
	if err := cmd.Run(); err == nil {
		return true
	}

	return false
}

// configureSparseCheckout sets up sparse checkout to only include .beads/
// Uses `git sparse-checkout` command which properly scopes the config to the
// worktree, avoiding GH#886 where core.sparseCheckout leaked to main repo.
func (wm *WorktreeManager) configureSparseCheckout(worktreePath string) error {
	// Initialize sparse checkout in non-cone mode (supports glob patterns)
	// This uses extensions.worktreeConfig to scope sparseCheckout to this worktree only
	initCmd := exec.Command("git", "sparse-checkout", "init", "--no-cone")
	initCmd.Dir = worktreePath
	output, err := initCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to init sparse checkout: %w\nOutput: %s", err, string(output))
	}

	// Set sparse checkout to only include .beads/
	setCmd := exec.Command("git", "sparse-checkout", "set", "/.beads/")
	setCmd.Dir = worktreePath
	output, err = setCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set sparse checkout patterns: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// NormalizeBeadsRelPath strips any leading path components before .beads/.
// This handles bare repo worktrees where the relative path includes the worktree
// name (e.g., "main/.beads/issues.jsonl" -> ".beads/issues.jsonl").
// GH#785, GH#810: Fix for sync failing across worktrees in bare repo setup.
func NormalizeBeadsRelPath(relPath string) string {
	// Use filepath.ToSlash for consistent handling across platforms
	normalized := filepath.ToSlash(relPath)
	// Look for ".beads/" to ensure we match the directory, not a prefix like ".beads-backup"
	if idx := strings.Index(normalized, ".beads/"); idx > 0 {
		// Strip leading path components before .beads
		return filepath.FromSlash(normalized[idx:])
	}
	return relPath
}

// verifySparseCheckout checks if sparse checkout is configured correctly
func (wm *WorktreeManager) verifySparseCheckout(worktreePath string) error {
	// Use git sparse-checkout list to verify configuration
	cmd := exec.Command("git", "sparse-checkout", "list")
	cmd.Dir = worktreePath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list sparse checkout patterns: %w\nOutput: %s", err, string(output))
	}

	// Verify it contains .beads
	if !strings.Contains(string(output), ".beads") {
		return fmt.Errorf("sparse-checkout does not include .beads")
	}

	return nil
}
