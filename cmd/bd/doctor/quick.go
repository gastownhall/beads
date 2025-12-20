package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// CheckSyncBranchQuick does a fast check for sync-branch configuration.
// Returns empty string if OK, otherwise returns issue description.
func CheckSyncBranchQuick() string {
	if syncbranch.IsConfigured() {
		return ""
	}
	return "sync-branch not configured in config.yaml"
}

// CheckHooksQuick does a fast check for outdated git hooks.
// Checks all beads hooks: pre-commit, post-merge, pre-push, post-checkout.
// cliVersion is the current CLI version to compare against.
func CheckHooksQuick(cliVersion string) string {
	// Get actual git directory (handles worktrees where .git is a file)
	gitDir, err := git.GetGitDir()
	if err != nil {
		return "" // Not a git repo, skip
	}
	hooksDir := filepath.Join(gitDir, "hooks")

	// Check if hooks dir exists
	if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
		return "" // No git hooks directory, skip
	}

	// Check all beads-managed hooks
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}

	var outdatedHooks []string
	var oldestVersion string

	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		content, err := os.ReadFile(hookPath) // #nosec G304 - path is controlled
		if err != nil {
			continue // Hook doesn't exist, skip (will be caught by full doctor)
		}

		// Look for version marker
		hookContent := string(content)
		if !strings.Contains(hookContent, "bd-hooks-version:") {
			continue // Not a bd hook or old format, skip
		}

		// Extract version
		for _, line := range strings.Split(hookContent, "\n") {
			if strings.Contains(line, "bd-hooks-version:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					hookVersion := strings.TrimSpace(parts[1])
					if hookVersion != cliVersion {
						outdatedHooks = append(outdatedHooks, hookName)
						// Track the oldest version for display
						if oldestVersion == "" || CompareVersions(hookVersion, oldestVersion) < 0 {
							oldestVersion = hookVersion
						}
					}
				}
				break
			}
		}
	}

	if len(outdatedHooks) == 0 {
		return ""
	}

	// Return summary of outdated hooks
	if len(outdatedHooks) == 1 {
		return fmt.Sprintf("Git hook %s outdated (%s → %s)", outdatedHooks[0], oldestVersion, cliVersion)
	}
	return fmt.Sprintf("Git hooks outdated: %s (%s → %s)", strings.Join(outdatedHooks, ", "), oldestVersion, cliVersion)
}

// CheckSyncBranchHookQuick does a fast check for sync-branch hook compatibility.
// Returns empty string if OK, otherwise returns issue description.
func CheckSyncBranchHookQuick(path string) string {
	// Check if sync-branch is configured
	syncBranch := syncbranch.GetFromYAML()
	if syncBranch == "" {
		return "" // sync-branch not configured, nothing to check
	}

	// Get git directory
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return "" // Not a git repo, skip
	}
	gitDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}

	// Find pre-push hook (check shared hooks first)
	var hookPath string
	hooksPathCmd := exec.Command("git", "config", "--get", "core.hooksPath")
	hooksPathCmd.Dir = path
	if hooksPathOutput, err := hooksPathCmd.Output(); err == nil {
		sharedHooksDir := strings.TrimSpace(string(hooksPathOutput))
		if !filepath.IsAbs(sharedHooksDir) {
			sharedHooksDir = filepath.Join(path, sharedHooksDir)
		}
		hookPath = filepath.Join(sharedHooksDir, "pre-push")
	} else {
		hookPath = filepath.Join(gitDir, "hooks", "pre-push")
	}

	content, err := os.ReadFile(hookPath) // #nosec G304 - path is controlled
	if err != nil {
		return "" // No pre-push hook, covered by other checks
	}

	// Check if bd hook and extract version
	hookStr := string(content)
	if !strings.Contains(hookStr, "bd-hooks-version:") {
		return "" // Not a bd hook, can't check
	}

	var hookVersion string
	for _, line := range strings.Split(hookStr, "\n") {
		if strings.Contains(line, "bd-hooks-version:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				hookVersion = strings.TrimSpace(parts[1])
			}
			break
		}
	}

	if hookVersion == "" {
		return "" // Can't determine version
	}

	// Check if version < MinSyncBranchHookVersion (when sync-branch bypass was added)
	if CompareVersions(hookVersion, MinSyncBranchHookVersion) < 0 {
		return fmt.Sprintf("Pre-push hook (%s) incompatible with sync-branch mode (requires %s+)", hookVersion, MinSyncBranchHookVersion)
	}

	return ""
}
