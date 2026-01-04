package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SyncBranchConfig fixes missing sync.branch configuration by auto-setting it to the current branch
func SyncBranchConfig(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Get current branch
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	currentBranch := strings.TrimSpace(string(output))
	if currentBranch == "" {
		return fmt.Errorf("current branch is empty")
	}

	// Get bd binary
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Set sync.branch using bd config set
	setCmd := newBdCmd(bdBinary, "config", "set", "sync.branch", currentBranch)
	setCmd.Dir = path
	if output, err := setCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set sync.branch: %w\nOutput: %s", err, string(output))
	}

	fmt.Printf("  Set sync.branch = %s\n", currentBranch)
	return nil
}

// SyncBranchHealth fixes a stale or diverged sync branch by resetting it to main.
// This handles two cases:
// 1. Local sync branch diverged from remote (after force-push)
// 2. Sync branch far behind main on source files
func SyncBranchHealth(path, syncBranch string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Determine main branch
	mainBranch := "main"
	cmd := exec.Command("git", "rev-parse", "--verify", "main")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("git", "rev-parse", "--verify", "master")
		cmd.Dir = path
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cannot determine main branch (neither main nor master exists)")
		}
		mainBranch = "master"
	}

	// Check if there's a worktree for this branch
	worktreePath := ""
	cmd = exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = path
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "worktree ") {
				wt := strings.TrimPrefix(line, "worktree ")
				// Check if next line has the branch
				if i+2 < len(lines) && strings.Contains(lines[i+2], syncBranch) {
					worktreePath = wt
					break
				}
			}
		}
	}

	// If worktree exists, reset within it
	if worktreePath != "" {
		fmt.Printf("  Resetting sync branch in worktree: %s\n", worktreePath)
		cmd = exec.Command("git", "fetch", "origin", mainBranch)
		cmd.Dir = worktreePath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to fetch: %w\n%s", err, out)
		}

		cmd = exec.Command("git", "reset", "--hard", fmt.Sprintf("origin/%s", mainBranch))
		cmd.Dir = worktreePath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to reset worktree: %w\n%s", err, out)
		}

		// Push the reset branch
		cmd = exec.Command("git", "push", "--force-with-lease", "origin", syncBranch)
		cmd.Dir = worktreePath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to push: %w\n%s", err, out)
		}

		fmt.Printf("  ✓ Reset %s to %s and pushed\n", syncBranch, mainBranch)
		return nil
	}

	// No worktree - reset the branch directly
	// First, make sure we're not on the sync branch
	cmd = exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = path
	currentBranchOutput, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(currentBranchOutput)) == syncBranch {
		return fmt.Errorf("currently on %s branch - checkout a different branch first", syncBranch)
	}

	// Delete and recreate the branch from main
	fmt.Printf("  Deleting local %s branch...\n", syncBranch)
	cmd = exec.Command("git", "branch", "-D", syncBranch)
	cmd.Dir = path
	_ = cmd.Run() // Ignore error if branch doesn't exist

	// Fetch latest and recreate
	cmd = exec.Command("git", "fetch", "origin", mainBranch)
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "branch", syncBranch, fmt.Sprintf("origin/%s", mainBranch))
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create branch: %w\n%s", err, out)
	}

	// Push the new branch
	cmd = exec.Command("git", "push", "--force-with-lease", "origin", syncBranch)
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to push: %w\n%s", err, out)
	}

	fmt.Printf("  ✓ Recreated %s from %s and pushed\n", syncBranch, mainBranch)
	return nil
}

// SyncBranchGitignore sets git index flags to hide .beads/issues.jsonl from git status
// when sync.branch is configured. This prevents the file from showing as modified on
// the main branch while actual data lives on the sync branch. (GH#797, GH#801, GH#870)
//
// Sets both flags for comprehensive hiding:
// - assume-unchanged: Performance optimization, skips file stat check
// - skip-worktree: Clear error message if user tries explicit `git add`
func SyncBranchGitignore(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Find the .beads directory
	beadsDir := filepath.Join(path, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf(".beads directory not found at %s", beadsDir)
	}

	// Check if issues.jsonl exists and is tracked by git
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		// File doesn't exist, nothing to hide
		return nil
	}

	// Check if file is tracked by git
	cmd := exec.Command("git", "ls-files", "--error-unmatch", jsonlPath)
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		// File is not tracked - add to .git/info/exclude instead
		return addToGitExclude(path, ".beads/issues.jsonl")
	}

	// File is tracked - set both git index flags
	// These must be separate commands (git quirk)
	cmd = exec.Command("git", "update-index", "--assume-unchanged", jsonlPath)
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set assume-unchanged: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "update-index", "--skip-worktree", jsonlPath)
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		// Revert assume-unchanged if skip-worktree fails
		revertCmd := exec.Command("git", "update-index", "--no-assume-unchanged", jsonlPath)
		revertCmd.Dir = path
		_ = revertCmd.Run()
		return fmt.Errorf("failed to set skip-worktree: %w\n%s", err, out)
	}

	fmt.Println("  ✓ Set git index flags to hide .beads/issues.jsonl")
	return nil
}

// ClearSyncBranchGitignore removes git index flags from .beads/issues.jsonl.
// Called when sync.branch is disabled to restore normal git tracking.
func ClearSyncBranchGitignore(path string) error {
	beadsDir := filepath.Join(path, ".beads")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return nil // File doesn't exist, nothing to do
	}

	// Check if file is tracked
	cmd := exec.Command("git", "ls-files", "--error-unmatch", jsonlPath)
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return nil // Not tracked, nothing to clear
	}

	// Clear both flags
	cmd = exec.Command("git", "update-index", "--no-assume-unchanged", jsonlPath)
	cmd.Dir = path
	_ = cmd.Run() // Ignore errors - flag might not be set

	cmd = exec.Command("git", "update-index", "--no-skip-worktree", jsonlPath)
	cmd.Dir = path
	_ = cmd.Run() // Ignore errors - flag might not be set

	return nil
}

// HasSyncBranchGitignoreFlags checks if git index flags are set on .beads/issues.jsonl
func HasSyncBranchGitignoreFlags(path string) (bool, bool, error) {
	beadsDir := filepath.Join(path, ".beads")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return false, false, nil
	}

	// Get file status from git ls-files -v
	// 'h' = assume-unchanged, 'S' = skip-worktree
	cmd := exec.Command("git", "ls-files", "-v", jsonlPath)
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return false, false, nil // File not tracked
	}

	line := strings.TrimSpace(string(output))
	if len(line) == 0 {
		return false, false, nil
	}

	// First character indicates status:
	// 'H' = tracked, 'h' = assume-unchanged, 'S' = skip-worktree
	firstChar := line[0]
	hasAssumeUnchanged := firstChar == 'h'
	hasSkipWorktree := firstChar == 'S'

	return hasAssumeUnchanged, hasSkipWorktree, nil
}

// addToGitExclude adds a pattern to .git/info/exclude
func addToGitExclude(path, pattern string) error {
	// Get git directory
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get git directory: %w", err)
	}

	gitDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}

	excludePath := filepath.Join(gitDir, "info", "exclude")

	// Create info directory if needed
	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return fmt.Errorf("failed to create info directory: %w", err)
	}

	// Check if pattern already exists
	content, _ := os.ReadFile(excludePath)
	if strings.Contains(string(content), pattern) {
		return nil // Already excluded
	}

	// Append pattern
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open exclude file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(pattern + "\n"); err != nil {
		return fmt.Errorf("failed to write exclude pattern: %w", err)
	}

	fmt.Printf("  ✓ Added %s to .git/info/exclude\n", pattern)
	return nil
}
