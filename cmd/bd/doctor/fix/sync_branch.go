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
	setCmd := exec.Command(bdBinary, "config", "set", "sync.branch", currentBranch)
	setCmd.Dir = path
	if output, err := setCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set sync.branch: %w\nOutput: %s", err, string(output))
	}

	fmt.Printf("  Set sync.branch = %s\n", currentBranch)
	return nil
}
