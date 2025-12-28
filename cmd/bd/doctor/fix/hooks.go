package fix

import (
	"fmt"
	"os"
	"os/exec"
)

// GitHooks fixes missing or broken git hooks by calling bd hooks install
func GitHooks(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Check if we're in a git repository using git rev-parse
	// This handles worktrees where .git is a file, not a directory
	checkCmd := exec.Command("git", "rev-parse", "--git-dir")
	checkCmd.Dir = path
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("not a git repository")
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Run bd hooks install
	cmd := newBdCmd(bdBinary, "hooks", "install")
	cmd.Dir = path                                     // Set working directory without changing process dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install hooks: %w", err)
	}

	return nil
}
