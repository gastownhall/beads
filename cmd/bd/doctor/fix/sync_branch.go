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

	// Process both JSONL files that need hiding
	filesToHide := []string{"issues.jsonl", "interactions.jsonl"}
	anyChanged := false

	for _, filename := range filesToHide {
		jsonlPath := filepath.Join(beadsDir, filename)
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			continue // File doesn't exist, skip
		}

		changed, err := setGitIndexFlags(path, jsonlPath, ".beads/"+filename)
		if err != nil {
			return err
		}
		if changed {
			anyChanged = true
		}
	}

	if anyChanged {
		fmt.Println("  ✓ Set git index flags to hide .beads/*.jsonl from git status")
	}
	return nil
}

// setGitIndexFlags sets assume-unchanged and skip-worktree flags on a file.
// Returns true if flags were changed, false if already set or file not tracked.
func setGitIndexFlags(repoPath, filePath, excludePattern string) (bool, error) {
	// Check if file is tracked by git
	cmd := exec.Command("git", "ls-files", "--error-unmatch", filePath)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		// File is not tracked - add to .git/info/exclude instead
		return false, addToGitExclude(repoPath, excludePattern)
	}

	// Check if flags are already set (skip-worktree takes precedence in ls-files -v output)
	cmd = exec.Command("git", "ls-files", "-v", filePath)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err == nil {
		line := strings.TrimSpace(string(output))
		if len(line) > 0 {
			firstChar := line[0]
			// 'S' = skip-worktree (our target state), 'h' = assume-unchanged only
			if firstChar == 'S' {
				return false, nil // Already has skip-worktree, nothing to do
			}
		}
	}

	// Set both git index flags (must be separate commands - git quirk)
	cmd = exec.Command("git", "update-index", "--assume-unchanged", filePath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("failed to set assume-unchanged on %s: %w\n%s", filePath, err, out)
	}

	cmd = exec.Command("git", "update-index", "--skip-worktree", filePath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		// Revert assume-unchanged if skip-worktree fails
		revertCmd := exec.Command("git", "update-index", "--no-assume-unchanged", filePath)
		revertCmd.Dir = repoPath
		_ = revertCmd.Run()
		return false, fmt.Errorf("failed to set skip-worktree on %s: %w\n%s", filePath, err, out)
	}

	return true, nil
}

// ClearSyncBranchGitignore removes git index flags from .beads/*.jsonl files.
// Called when sync.branch is disabled to restore normal git tracking.
func ClearSyncBranchGitignore(path string) error {
	beadsDir := filepath.Join(path, ".beads")
	filesToClear := []string{"issues.jsonl", "interactions.jsonl"}

	for _, filename := range filesToClear {
		jsonlPath := filepath.Join(beadsDir, filename)

		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			continue // File doesn't exist, skip
		}

		// Check if file is tracked
		cmd := exec.Command("git", "ls-files", "--error-unmatch", jsonlPath)
		cmd.Dir = path
		if err := cmd.Run(); err != nil {
			continue // Not tracked, skip
		}

		// Clear both flags (ignore errors - flags might not be set)
		cmd = exec.Command("git", "update-index", "--no-assume-unchanged", jsonlPath)
		cmd.Dir = path
		_ = cmd.Run()

		cmd = exec.Command("git", "update-index", "--no-skip-worktree", jsonlPath)
		cmd.Dir = path
		_ = cmd.Run()
	}

	return nil
}

// HasSyncBranchGitignoreFlags checks if git index flags are set on .beads/issues.jsonl.
// Returns (hasAnyFlag, hasSkipWorktree, error).
// Note: When both assume-unchanged and skip-worktree are set, git shows 'S' (skip-worktree
// takes precedence). So hasAnyFlag being true means the file is hidden from git status.
func HasSyncBranchGitignoreFlags(path string) (bool, bool, error) {
	beadsDir := filepath.Join(path, ".beads")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return false, false, nil
	}

	// Get file status from git ls-files -v
	// 'H' = tracked normally, 'h' = assume-unchanged, 'S' = skip-worktree
	// When both flags are set, 'S' is shown (skip-worktree takes precedence)
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

	firstChar := line[0]
	// 'h' = assume-unchanged only, 'S' = skip-worktree (possibly with assume-unchanged too)
	hasAnyFlag := firstChar == 'h' || firstChar == 'S'
	hasSkipWorktree := firstChar == 'S'

	return hasAnyFlag, hasSkipWorktree, nil
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

	// Check if pattern already exists (exact line match)
	content, _ := os.ReadFile(excludePath)
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == pattern {
			return nil // Already excluded
		}
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
