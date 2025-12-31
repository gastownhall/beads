package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/ui"
)

// hooksInstalled checks if bd git hooks are installed
func hooksInstalled() bool {
	gitDir, err := git.GetGitDir()
	if err != nil {
		return false
	}
	preCommit := filepath.Join(gitDir, "hooks", "pre-commit")
	postMerge := filepath.Join(gitDir, "hooks", "post-merge")

	// Check if both hooks exist
	_, err1 := os.Stat(preCommit)
	_, err2 := os.Stat(postMerge)

	if err1 != nil || err2 != nil {
		return false
	}

	// Verify they're bd hooks by checking for signature comment
	// #nosec G304 - controlled path from git directory
	preCommitContent, err := os.ReadFile(preCommit)
	if err != nil || !strings.Contains(string(preCommitContent), "bd (beads) pre-commit hook") {
		return false
	}

	// #nosec G304 - controlled path from git directory
	postMergeContent, err := os.ReadFile(postMerge)
	if err != nil || !strings.Contains(string(postMergeContent), "bd (beads) post-merge hook") {
		return false
	}

	// Verify hooks are executable
	preCommitInfo, err := os.Stat(preCommit)
	if err != nil {
		return false
	}
	if preCommitInfo.Mode().Perm()&0111 == 0 {
		return false // Not executable
	}

	postMergeInfo, err := os.Stat(postMerge)
	if err != nil {
		return false
	}
	if postMergeInfo.Mode().Perm()&0111 == 0 {
		return false // Not executable
	}

	return true
}

// hookInfo contains information about an existing hook
type hookInfo struct {
	name        string
	path        string
	exists      bool
	isBdHook    bool
	isPreCommit bool
	content     string
}

// detectExistingHooks scans for existing git hooks
func detectExistingHooks() []hookInfo {
	gitDir, err := git.GetGitDir()
	if err != nil {
		return nil
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	hooks := []hookInfo{
		{name: "pre-commit", path: filepath.Join(hooksDir, "pre-commit")},
		{name: "post-merge", path: filepath.Join(hooksDir, "post-merge")},
		{name: "pre-push", path: filepath.Join(hooksDir, "pre-push")},
	}

	for i := range hooks {
		content, err := os.ReadFile(hooks[i].path)
		if err == nil {
			hooks[i].exists = true
			hooks[i].content = string(content)
			hooks[i].isBdHook = strings.Contains(hooks[i].content, "bd (beads)")
			// Only detect pre-commit framework if not a bd hook
			if !hooks[i].isBdHook {
				hooks[i].isPreCommit = strings.Contains(hooks[i].content, "pre-commit run") ||
					strings.Contains(hooks[i].content, ".pre-commit-config")
			}
		}
	}

	return hooks
}

// promptHookAction asks user what to do with existing hooks
func promptHookAction(existingHooks []hookInfo) string {
	fmt.Printf("\n%s Found existing git hooks:\n", ui.RenderWarn("⚠"))
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hookType := "custom script"
			if hook.isPreCommit {
				hookType = "pre-commit framework"
			}
			fmt.Printf("  - %s (%s)\n", hook.name, hookType)
		}
	}

	fmt.Printf("\nHow should bd proceed?\n")
	fmt.Printf("  [1] Chain with existing hooks (recommended)\n")
	fmt.Printf("  [2] Overwrite existing hooks\n")
	fmt.Printf("  [3] Skip git hooks installation\n")
	fmt.Printf("Choice [1-3]: ")

	var response string
	_, _ = fmt.Scanln(&response)
	response = strings.TrimSpace(response)

	return response
}

// installGitHooks installs git hooks inline (no external dependencies)
func installGitHooks() error {
	gitDir, err := git.GetGitDir()
	if err != nil {
		return err
	}
	hooksDir := filepath.Join(gitDir, "hooks")

	// Ensure hooks directory exists
	if err := os.MkdirAll(hooksDir, 0750); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// Detect existing hooks
	existingHooks := detectExistingHooks()

	// Check if any non-bd hooks exist
	hasExistingHooks := false
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hasExistingHooks = true
			break
		}
	}

	// Determine installation mode
	chainHooks := false
	if hasExistingHooks {
		choice := promptHookAction(existingHooks)
		switch choice {
		case "1", "":
			chainHooks = true
			// Chain mode - rename existing hooks to .old so they can be called
			for _, hook := range existingHooks {
				if hook.exists && !hook.isBdHook {
					oldPath := hook.path + ".old"
					if err := os.Rename(hook.path, oldPath); err != nil {
						return fmt.Errorf("failed to rename %s to .old: %w", hook.name, err)
					}
					fmt.Printf("  Renamed %s to %s\n", hook.name, filepath.Base(oldPath))
				}
			}
		case "2":
			// Overwrite mode - backup existing hooks
			for _, hook := range existingHooks {
				if hook.exists && !hook.isBdHook {
					timestamp := time.Now().Format("20060102-150405")
					backup := hook.path + ".backup-" + timestamp
					if err := os.Rename(hook.path, backup); err != nil {
						return fmt.Errorf("failed to backup %s: %w", hook.name, err)
					}
					fmt.Printf("  Backed up %s to %s\n", hook.name, filepath.Base(backup))
				}
			}
		case "3":
			fmt.Printf("Skipping git hooks installation.\n")
			fmt.Printf("You can install manually later with: %s\n", ui.RenderAccent("./examples/git-hooks/install.sh"))
			return nil
		default:
			return fmt.Errorf("invalid choice: %s", choice)
		}
	}

	// pre-commit hook
	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	preCommitContent := buildPreCommitHook(chainHooks, existingHooks)

	// post-merge hook
	postMergePath := filepath.Join(hooksDir, "post-merge")
	postMergeContent := buildPostMergeHook(chainHooks, existingHooks)

	// Write pre-commit hook (executable scripts need 0700)
	// #nosec G306 - git hooks must be executable
	if err := os.WriteFile(preCommitPath, []byte(preCommitContent), 0700); err != nil {
		return fmt.Errorf("failed to write pre-commit hook: %w", err)
	}

	// Write post-merge hook (executable scripts need 0700)
	// #nosec G306 - git hooks must be executable
	if err := os.WriteFile(postMergePath, []byte(postMergeContent), 0700); err != nil {
		return fmt.Errorf("failed to write post-merge hook: %w", err)
	}

	if chainHooks {
		fmt.Printf("%s Chained bd hooks with existing hooks\n", ui.RenderPass("✓"))
	}

	return nil
}

// buildPreCommitHook generates the pre-commit hook content
func buildPreCommitHook(chainHooks bool, existingHooks []hookInfo) string {
	if chainHooks {
		// Find existing pre-commit hook (already renamed to .old by caller)
		var existingPreCommit string
		for _, hook := range existingHooks {
			if hook.name == "pre-commit" && hook.exists && !hook.isBdHook {
				existingPreCommit = hook.path + ".old"
				break
			}
		}

		return `#!/bin/sh
#
# bd (beads) pre-commit hook (chained)
#
# This hook chains bd functionality with your existing pre-commit hook.

# Run existing hook first
if [ -x "` + existingPreCommit + `" ]; then
    "` + existingPreCommit + `" "$@"
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
        exit $EXIT_CODE
    fi
fi

` + preCommitHookBody()
	}

	return `#!/bin/sh
#
# bd (beads) pre-commit hook
#
# This hook ensures that any pending bd issue changes are flushed to
# .beads/issues.jsonl before the commit is created, preventing the
# race condition where daemon auto-flush fires after the commit.

` + preCommitHookBody()
}

// preCommitHookBody returns the common pre-commit hook logic
func preCommitHookBody() string {
	return `# Check if bd is available
if ! command -v bd >/dev/null 2>&1; then
    echo "Warning: bd command not found, skipping pre-commit flush" >&2
    exit 0
fi

# Check if we're in a bd workspace
# For worktrees, .beads is in the main repository root, not the worktree
BEADS_DIR=""
if git rev-parse --git-dir >/dev/null 2>&1; then
    # Check if we're in a worktree
    if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
        # Worktree: .beads is in main repo root
        MAIN_REPO_ROOT="$(git rev-parse --git-common-dir)"
        MAIN_REPO_ROOT="$(dirname "$MAIN_REPO_ROOT")"
        if [ -d "$MAIN_REPO_ROOT/.beads" ]; then
            BEADS_DIR="$MAIN_REPO_ROOT/.beads"
        fi
    else
        # Regular repo: check current directory
        if [ -d .beads ]; then
            BEADS_DIR=".beads"
        fi
    fi
fi

if [ -z "$BEADS_DIR" ]; then
    exit 0
fi

# Flush pending changes to JSONL
if ! bd sync --flush-only >/dev/null 2>&1; then
    echo "Error: Failed to flush bd changes to JSONL" >&2
    echo "Run 'bd sync --flush-only' manually to diagnose" >&2
    exit 1
fi

# If the JSONL file was modified, stage it
# For worktrees, the JSONL is in the main repo's working tree, not the worktree,
# so we can't use git add. Skip this step for worktrees.
if [ -f "$BEADS_DIR/issues.jsonl" ]; then
    if [ "$(git rev-parse --git-dir)" = "$(git rev-parse --git-common-dir)" ]; then
        # Regular repo: file is in the working tree, safe to add
        git add "$BEADS_DIR/issues.jsonl" 2>/dev/null || true
    fi
    # For worktrees: .beads is in the main repo's working tree, not this worktree
    # Git rejects adding files outside the worktree, so we skip it.
    # The main repo will see the changes on the next pull/sync.
fi

exit 0
`
}

// buildPostMergeHook generates the post-merge hook content
func buildPostMergeHook(chainHooks bool, existingHooks []hookInfo) string {
	if chainHooks {
		// Find existing post-merge hook (already renamed to .old by caller)
		var existingPostMerge string
		for _, hook := range existingHooks {
			if hook.name == "post-merge" && hook.exists && !hook.isBdHook {
				existingPostMerge = hook.path + ".old"
				break
			}
		}

		return `#!/bin/sh
#
# bd (beads) post-merge hook (chained)
#
# This hook chains bd functionality with your existing post-merge hook.

# Run existing hook first
if [ -x "` + existingPostMerge + `" ]; then
    "` + existingPostMerge + `" "$@"
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
        exit $EXIT_CODE
    fi
fi

` + postMergeHookBody()
	}

	return `#!/bin/sh
#
# bd (beads) post-merge hook
#
# This hook imports updated issues from .beads/issues.jsonl after a
# git pull or merge, ensuring the database stays in sync with git.

` + postMergeHookBody()
}

// postMergeHookBody returns the common post-merge hook logic
func postMergeHookBody() string {
	return `# Check if bd is available
if ! command -v bd >/dev/null 2>&1; then
    echo "Warning: bd command not found, skipping post-merge import" >&2
    exit 0
fi

# Check if we're in a bd workspace
# For worktrees, .beads is in the main repository root, not the worktree
BEADS_DIR=""
if git rev-parse --git-dir >/dev/null 2>&1; then
    # Check if we're in a worktree
    if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
        # Worktree: .beads is in main repo root
        MAIN_REPO_ROOT="$(git rev-parse --git-common-dir)"
        MAIN_REPO_ROOT="$(dirname "$MAIN_REPO_ROOT")"
        if [ -d "$MAIN_REPO_ROOT/.beads" ]; then
            BEADS_DIR="$MAIN_REPO_ROOT/.beads"
        fi
    else
        # Regular repo: check current directory
        if [ -d .beads ]; then
            BEADS_DIR=".beads"
        fi
    fi
fi

if [ -z "$BEADS_DIR" ]; then
    exit 0
fi

# Check if issues.jsonl exists and was updated
if [ ! -f "$BEADS_DIR/issues.jsonl" ]; then
    exit 0
fi

# Import the updated JSONL
if ! bd import -i "$BEADS_DIR/issues.jsonl" >/dev/null 2>&1; then
    echo "Warning: Failed to import bd changes after merge" >&2
    echo "Run 'bd import -i $BEADS_DIR/issues.jsonl' manually to see the error" >&2
fi

exit 0
`
}

// mergeDriverInstalled checks if bd merge driver is configured correctly
func mergeDriverInstalled() bool {
	// Check git config for merge driver
	cmd := exec.Command("git", "config", "merge.beads.driver")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return false
	}

	// Check if using old invalid placeholders (%L/%R from versions <0.24.0)
	// Git only supports %O (base), %A (current), %B (other)
	driverConfig := strings.TrimSpace(string(output))
	if strings.Contains(driverConfig, "%L") || strings.Contains(driverConfig, "%R") {
		// Stale config with invalid placeholders - needs repair
		return false
	}

	// Check if .gitattributes has the merge driver configured
	gitattributesPath := ".gitattributes"
	content, err := os.ReadFile(gitattributesPath)
	if err != nil {
		return false
	}

	// Look for beads JSONL merge attribute (either canonical or legacy filename)
	hasCanonical := strings.Contains(string(content), ".beads/issues.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	hasLegacy := strings.Contains(string(content), ".beads/beads.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	return hasCanonical || hasLegacy
}

// installMergeDriver configures git to use bd merge for JSONL files
func installMergeDriver() error {
	// Configure git merge driver
	cmd := exec.Command("git", "config", "merge.beads.driver", "bd merge %A %O %A %B")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to configure git merge driver: %w\n%s", err, output)
	}

	cmd = exec.Command("git", "config", "merge.beads.name", "bd JSONL merge driver")
	if output, err := cmd.CombinedOutput(); err != nil {
		// Non-fatal, the name is just descriptive
		fmt.Fprintf(os.Stderr, "Warning: failed to set merge driver name: %v\n%s", err, output)
	}

	// Create or update .gitattributes
	gitattributesPath := ".gitattributes"

	// Read existing .gitattributes if it exists
	var existingContent string
	content, err := os.ReadFile(gitattributesPath)
	if err == nil {
		existingContent = string(content)
	}

	// Check if beads merge driver is already configured
	// Check for either pattern (issues.jsonl is canonical, beads.jsonl is legacy)
	hasBeadsMerge := (strings.Contains(existingContent, ".beads/issues.jsonl") ||
		strings.Contains(existingContent, ".beads/beads.jsonl")) &&
		strings.Contains(existingContent, "merge=beads")

	if !hasBeadsMerge {
		// Append beads merge driver configuration (issues.jsonl is canonical)
		beadsMergeAttr := "\n# Use bd merge for beads JSONL files\n.beads/issues.jsonl merge=beads\n"

		newContent := existingContent
		if !strings.HasSuffix(newContent, "\n") && len(newContent) > 0 {
			newContent += "\n"
		}
		newContent += beadsMergeAttr

		// Write updated .gitattributes (0644 is standard for .gitattributes)
		// #nosec G306 - .gitattributes needs to be readable
		if err := os.WriteFile(gitattributesPath, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("failed to update .gitattributes: %w", err)
		}
	}

	return nil
}
