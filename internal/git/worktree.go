package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
			return nil // Already exists and is valid
		}
		// Path exists but isn't a valid worktree, remove it
		if err := os.RemoveAll(worktreePath); err != nil {
			return fmt.Errorf("failed to remove invalid worktree path: %w", err)
		}
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0750); err != nil {
		return fmt.Errorf("failed to create worktree parent directory: %w", err)
	}

	// Check if branch exists remotely or locally
	branchExists := wm.branchExists(branch)

	// Create worktree without checking out files initially
	var cmd *exec.Cmd
	if branchExists {
		// Checkout existing branch
		cmd = exec.Command("git", "worktree", "add", "--no-checkout", worktreePath, branch)
	} else {
		// Create new branch
		cmd = exec.Command("git", "worktree", "add", "--no-checkout", "-b", branch, worktreePath)
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

// SyncJSONLToWorktree copies the JSONL file from main repo to worktree
func (wm *WorktreeManager) SyncJSONLToWorktree(worktreePath, jsonlRelPath string) error {
	// Source: main repo JSONL
	srcPath := filepath.Join(wm.repoPath, jsonlRelPath)
	
	// Destination: worktree JSONL
	dstPath := filepath.Join(worktreePath, jsonlRelPath)

	// Ensure destination directory exists
	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0750); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Read source file
	data, err := os.ReadFile(srcPath) // #nosec G304 - controlled path from config
	if err != nil {
		return fmt.Errorf("failed to read source JSONL: %w", err)
	}

	// Write to destination
	if err := os.WriteFile(dstPath, data, 0644); err != nil { // #nosec G306 - JSONL needs to be readable
		return fmt.Errorf("failed to write destination JSONL: %w", err)
	}

	return nil
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
			if absPath == absWorktreePath {
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
func (wm *WorktreeManager) configureSparseCheckout(worktreePath string) error {
	// Get the actual git directory (for worktrees, .git is a file)
	gitFile := filepath.Join(worktreePath, ".git")
	gitContent, err := os.ReadFile(gitFile) // #nosec G304 - controlled path
	if err != nil {
		return fmt.Errorf("failed to read .git file: %w", err)
	}

	// Parse "gitdir: /path/to/git/dir"
	gitDirLine := strings.TrimSpace(string(gitContent))
	if !strings.HasPrefix(gitDirLine, "gitdir: ") {
		return fmt.Errorf("invalid .git file format: %s", gitDirLine)
	}
	gitDir := strings.TrimPrefix(gitDirLine, "gitdir: ")

	// Enable sparse checkout config
	cmd := exec.Command("git", "config", "core.sparseCheckout", "true")
	cmd.Dir = worktreePath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to enable sparse checkout: %w\nOutput: %s", err, string(output))
	}

	// Create info directory if it doesn't exist
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0750); err != nil {
		return fmt.Errorf("failed to create info directory: %w", err)
	}

	// Write sparse-checkout file to include only .beads/
	sparseFile := filepath.Join(infoDir, "sparse-checkout")
	sparseContent := ".beads/*\n"
	if err := os.WriteFile(sparseFile, []byte(sparseContent), 0644); err != nil { // #nosec G306 - sparse-checkout config file needs standard permissions
		return fmt.Errorf("failed to write sparse-checkout file: %w", err)
	}

	return nil
}

// verifySparseCheckout checks if sparse checkout is configured correctly
func (wm *WorktreeManager) verifySparseCheckout(worktreePath string) error {
	// Check if sparse-checkout file exists and contains .beads
	sparseFile := filepath.Join(worktreePath, ".git", "info", "sparse-checkout")
	
	// For worktrees, .git is a file pointing to the actual git dir
	// We need to read the actual git directory location
	gitFile := filepath.Join(worktreePath, ".git")
	gitContent, err := os.ReadFile(gitFile) // #nosec G304 - controlled path
	if err != nil {
		return fmt.Errorf("failed to read .git file: %w", err)
	}

	// Parse "gitdir: /path/to/git/dir"
	gitDirLine := strings.TrimSpace(string(gitContent))
	if !strings.HasPrefix(gitDirLine, "gitdir: ") {
		return fmt.Errorf("invalid .git file format")
	}
	gitDir := strings.TrimPrefix(gitDirLine, "gitdir: ")
	
	// Sparse checkout file is in the git directory
	sparseFile = filepath.Join(gitDir, "info", "sparse-checkout")
	
	data, err := os.ReadFile(sparseFile) // #nosec G304 - controlled path
	if err != nil {
		return fmt.Errorf("sparse-checkout file not found: %w", err)
	}

	// Verify it contains .beads
	if !strings.Contains(string(data), ".beads") {
		return fmt.Errorf("sparse-checkout does not include .beads")
	}

	return nil
}
