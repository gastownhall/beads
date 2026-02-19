package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/git"
)

// CheckSyncBranchQuick is a no-op now that sync-branch is removed.
func CheckSyncBranchQuick() string {
	return ""
}

// CheckHooksQuick does a fast check for outdated git hooks.
// Checks all beads hooks: pre-commit, post-merge, pre-push, post-checkout.
// cliVersion is the current CLI version to compare against.
func CheckHooksQuick(cliVersion string) string {
	// Get hooks directory from common git dir (hooks are shared across worktrees)
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return "" // Not a git repo, skip
	}

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

// CheckSyncBranchHookQuick is a no-op now that sync-branch is removed.
func CheckSyncBranchHookQuick(path string) string {
	return ""
}
