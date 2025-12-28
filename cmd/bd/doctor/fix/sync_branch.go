package fix

import (
	"fmt"
	"os/exec"
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
