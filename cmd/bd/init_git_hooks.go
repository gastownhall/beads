package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/ui"
)

// preCommitFrameworkPattern matches pre-commit or prek framework hooks.
// Uses same patterns as hookManagerPatterns in doctor/fix/hooks.go for consistency.
// Includes all detection patterns: pre-commit run, prek run/hook-impl, config file refs, and pre-commit env vars.
var preCommitFrameworkPattern = regexp.MustCompile(`(?i)(pre-commit\s+run|prek\s+run|prek\s+hook-impl|\.pre-commit-config|INSTALL_PYTHON|PRE_COMMIT)`)

// hooksInstalled checks if bd git hooks are installed
func hooksInstalled() bool {
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return false
	}
	preCommit := filepath.Join(hooksDir, "pre-commit")
	postMerge := filepath.Join(hooksDir, "post-merge")

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
	name                 string
	path                 string
	exists               bool
	isBdHook             bool
	isPreCommitFramework bool // true for pre-commit or prek
	content              string
}

// detectExistingHooks scans for existing git hooks
func detectExistingHooks() []hookInfo {
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return nil
	}
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
			// Only detect pre-commit/prek framework if not a bd hook
			// Use regex for consistency with DetectActiveHookManager patterns
			if !hooks[i].isBdHook {
				hooks[i].isPreCommitFramework = preCommitFrameworkPattern.MatchString(hooks[i].content)
			}
		}
	}

	return hooks
}

// installGitHooks installs git hooks inline (no external dependencies)
func installGitHooks() error {
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return err
	}

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

	// Default to chaining with existing hooks (no prompting)
	chainHooks := hasExistingHooks
	if chainHooks {
		// Chain mode - rename existing hooks to .old so they can be called
		for _, hook := range existingHooks {
			if hook.exists && !hook.isBdHook {
				oldPath := hook.path + ".old"
				if err := os.Rename(hook.path, oldPath); err != nil {
					fmt.Fprintf(os.Stderr, "%s Failed to chain with existing %s hook: %v\n", ui.RenderWarn("⚠"), hook.name, err)
					fmt.Fprintf(os.Stderr, "You can resolve this with: %s\n", ui.RenderAccent("bd doctor --fix"))
					continue
				}
				fmt.Printf("  Chained with existing %s hook\n", hook.name)
			}
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

# Skip for Dolt backend (uses its own sync mechanism, not JSONL)
if [ -f "$BEADS_DIR/metadata.json" ]; then
    if grep -q '"backend"[[:space:]]*:[[:space:]]*"dolt"' "$BEADS_DIR/metadata.json" 2>/dev/null; then
        exit 0
    fi
fi

# Flush pending changes to JSONL
if ! bd sync --flush-only >/dev/null 2>&1; then
    echo "Error: Failed to flush bd changes to storage" >&2
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

# Skip for Dolt backend (uses its own sync mechanism, not JSONL import)
if [ -f "$BEADS_DIR/metadata.json" ]; then
    if grep -q '"backend"[[:space:]]*:[[:space:]]*"dolt"' "$BEADS_DIR/metadata.json" 2>/dev/null; then
        exit 0
    fi
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

// installJJHooks installs simplified git hooks for colocated jujutsu+git repos.
// jj's model is simpler: the working copy IS always a commit, so no staging needed.
// Changes flow into the current change automatically.
func installJJHooks() error {
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return err
	}

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

	// Default to chaining with existing hooks (no prompting)
	chainHooks := hasExistingHooks
	if chainHooks {
		// Chain mode - rename existing hooks to .old so they can be called
		for _, hook := range existingHooks {
			if hook.exists && !hook.isBdHook {
				oldPath := hook.path + ".old"
				if err := os.Rename(hook.path, oldPath); err != nil {
					fmt.Fprintf(os.Stderr, "%s Failed to chain with existing %s hook: %v\n", ui.RenderWarn("⚠"), hook.name, err)
					fmt.Fprintf(os.Stderr, "You can resolve this with: %s\n", ui.RenderAccent("bd doctor --fix"))
					continue
				}
				fmt.Printf("  Chained with existing %s hook\n", hook.name)
			}
		}
	}

	// pre-commit hook (simplified for jj - no staging)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	preCommitContent := buildJJPreCommitHook(chainHooks, existingHooks)

	// post-merge hook (same as git)
	postMergePath := filepath.Join(hooksDir, "post-merge")
	postMergeContent := buildPostMergeHook(chainHooks, existingHooks)

	// Write pre-commit hook
	// #nosec G306 - git hooks must be executable
	if err := os.WriteFile(preCommitPath, []byte(preCommitContent), 0700); err != nil {
		return fmt.Errorf("failed to write pre-commit hook: %w", err)
	}

	// Write post-merge hook
	// #nosec G306 - git hooks must be executable
	if err := os.WriteFile(postMergePath, []byte(postMergeContent), 0700); err != nil {
		return fmt.Errorf("failed to write post-merge hook: %w", err)
	}

	if chainHooks {
		fmt.Printf("%s Chained bd hooks with existing hooks (jj mode)\n", ui.RenderPass("✓"))
	}

	return nil
}

// buildJJPreCommitHook generates the pre-commit hook content for jujutsu repos.
// jj's model is simpler: no staging needed, changes flow into the working copy automatically.
func buildJJPreCommitHook(chainHooks bool, existingHooks []hookInfo) string {
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
# bd (beads) pre-commit hook (chained, jujutsu mode)
#
# This hook chains bd functionality with your existing pre-commit hook.
# Simplified for jujutsu: no staging needed, jj auto-commits working copy.

# Run existing hook first
if [ -x "` + existingPreCommit + `" ]; then
    "` + existingPreCommit + `" "$@"
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
        exit $EXIT_CODE
    fi
fi

` + jjPreCommitHookBody()
	}

	return `#!/bin/sh
#
# bd (beads) pre-commit hook (jujutsu mode)
#
# This hook ensures that any pending bd issue changes are flushed to
# .beads/issues.jsonl before the commit.
#
# Simplified for jujutsu: no staging needed, jj auto-commits working copy changes.

` + jjPreCommitHookBody()
}

// jjPreCommitHookBody returns the pre-commit hook logic for jujutsu repos.
// Key difference from git: no git add needed, jj handles working copy automatically.
// Still needs worktree handling since colocated jj+git repos can use git worktrees.
func jjPreCommitHookBody() string {
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
# In jujutsu, changes automatically become part of the working copy commit
if ! bd sync --flush-only >/dev/null 2>&1; then
    echo "Error: Failed to flush bd changes to storage" >&2
    echo "Run 'bd sync --flush-only' manually to diagnose" >&2
    exit 1
fi

# No git add needed - jujutsu automatically includes working copy changes
exit 0
`
}

// printJJAliasInstructions prints setup instructions for pure jujutsu repos.
// Since jj doesn't have native hooks yet, users need to set up aliases.
func printJJAliasInstructions() {
	fmt.Printf("\n%s Jujutsu repository detected (not colocated with git)\n\n", ui.RenderWarn("⚠"))
	fmt.Printf("Jujutsu doesn't support hooks yet. To auto-export beads on push,\n")
	fmt.Printf("add this alias to your jj config (~/.config/jj/config.toml):\n\n")
	fmt.Printf("  %s\n", ui.RenderAccent("[aliases]"))
	fmt.Printf("  %s\n", ui.RenderAccent(`push = ["util", "exec", "--", "sh", "-c", "bd sync --flush-only && jj git push \"$@\"", ""]`))
	fmt.Printf("\nThen use %s instead of %s\n\n", ui.RenderAccent("jj push"), ui.RenderAccent("jj git push"))
	fmt.Printf("For more details, see: https://github.com/steveyegge/beads/blob/main/docs/JUJUTSU.md\n\n")
}
