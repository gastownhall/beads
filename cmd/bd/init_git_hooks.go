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

	// Verify they're bd hooks by checking for signature comment or section marker
	// #nosec G304 - controlled path from git directory
	preCommitContent, err := os.ReadFile(preCommit)
	if err != nil {
		return false
	}
	preCommitStr := string(preCommitContent)
	if !strings.Contains(preCommitStr, "bd (beads) pre-commit hook") &&
		!strings.Contains(preCommitStr, hookSectionBeginPrefix) {
		return false
	}

	// #nosec G304 - controlled path from git directory
	postMergeContent, err := os.ReadFile(postMerge)
	if err != nil {
		return false
	}
	postMergeStr := string(postMergeContent)
	if !strings.Contains(postMergeStr, "bd (beads) post-merge hook") &&
		!strings.Contains(postMergeStr, hookSectionBeginPrefix) {
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

// hooksNeedUpdate checks if installed bd hooks are outdated and need updating.
// Delegates to CheckGitHooks() which handles version comparison, shim detection,
// and inline hook detection consistently.
func hooksNeedUpdate() bool {
	for _, s := range CheckGitHooks() {
		if s.Outdated {
			return true
		}
	}
	return false
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
			hooks[i].isBdHook = strings.Contains(hooks[i].content, "bd (beads)") ||
				strings.Contains(hooks[i].content, hookSectionBeginPrefix)
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

	// Normalize line endings to LF — on Windows/NTFS, Go string literals
	// are fine but concatenated content from other sources may have CRLF.
	// Git hooks with CRLF fail: /usr/bin/env: 'sh\r': No such file or directory
	preCommitContent = strings.ReplaceAll(preCommitContent, "\r\n", "\n")
	postMergeContent = strings.ReplaceAll(postMergeContent, "\r\n", "\n")

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

// buildPreCommitHook generates the pre-commit hook content using section markers (GH#1380).
// If chainHooks is true, chained hooks (.old) are called before the beads section.
func buildPreCommitHook(chainHooks bool, existingHooks []hookInfo) string {
	section := generateHookSection("pre-commit")

	if chainHooks {
		var existingPreCommit string
		for _, hook := range existingHooks {
			if hook.name == "pre-commit" && hook.exists && !hook.isBdHook {
				existingPreCommit = hook.path + ".old"
				break
			}
		}

		return "#!/bin/sh\n" +
			"# Run existing hook first\n" +
			"if [ -x \"" + existingPreCommit + "\" ]; then\n" +
			"    \"" + existingPreCommit + "\" \"$@\"\n" +
			"    EXIT_CODE=$?\n" +
			"    if [ $EXIT_CODE -ne 0 ]; then\n" +
			"        exit $EXIT_CODE\n" +
			"    fi\n" +
			"fi\n\n" +
			section
	}

	return "#!/bin/sh\n" + section
}

// buildPostMergeHook generates the post-merge hook content using section markers (GH#1380).
func buildPostMergeHook(chainHooks bool, existingHooks []hookInfo) string {
	section := generateHookSection("post-merge")

	if chainHooks {
		var existingPostMerge string
		for _, hook := range existingHooks {
			if hook.name == "post-merge" && hook.exists && !hook.isBdHook {
				existingPostMerge = hook.path + ".old"
				break
			}
		}

		return "#!/bin/sh\n" +
			"# Run existing hook first\n" +
			"if [ -x \"" + existingPostMerge + "\" ]; then\n" +
			"    \"" + existingPostMerge + "\" \"$@\"\n" +
			"    EXIT_CODE=$?\n" +
			"    if [ $EXIT_CODE -ne 0 ]; then\n" +
			"        exit $EXIT_CODE\n" +
			"    fi\n" +
			"fi\n\n" +
			section
	}

	return "#!/bin/sh\n" + section
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

// buildJJPreCommitHook generates the pre-commit hook for jujutsu repos using section markers (GH#1380).
func buildJJPreCommitHook(chainHooks bool, existingHooks []hookInfo) string {
	// jj uses the same shim as git — bd hooks run handles the differences internally
	section := generateHookSection("pre-commit")

	if chainHooks {
		var existingPreCommit string
		for _, hook := range existingHooks {
			if hook.name == "pre-commit" && hook.exists && !hook.isBdHook {
				existingPreCommit = hook.path + ".old"
				break
			}
		}

		return "#!/bin/sh\n" +
			"# Run existing hook first\n" +
			"if [ -x \"" + existingPreCommit + "\" ]; then\n" +
			"    \"" + existingPreCommit + "\" \"$@\"\n" +
			"    EXIT_CODE=$?\n" +
			"    if [ $EXIT_CODE -ne 0 ]; then\n" +
			"        exit $EXIT_CODE\n" +
			"    fi\n" +
			"fi\n\n" +
			section
	}

	return "#!/bin/sh\n" + section
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
