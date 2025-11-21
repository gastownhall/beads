package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GitHooks fixes missing or broken git hooks by calling bd hooks install
func GitHooks(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Check if we're in a git repository
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("not a git repository")
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Run bd hooks install
	cmd := exec.Command(bdBinary, "hooks", "install") // #nosec G204 -- bdBinary from validated executable path
	cmd.Dir = path                                     // Set working directory without changing process dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install hooks: %w", err)
	}

	return nil
}
