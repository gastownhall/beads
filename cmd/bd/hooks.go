package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/ui"
)

// managedHookNames lists the git hooks managed by beads.
// Hook content is generated dynamically by generateHookSection().
var managedHookNames = []string{"pre-commit", "post-merge", "pre-push", "post-checkout", "prepare-commit-msg"}

const hookVersionPrefix = "# bd-hooks-version: "
const shimVersionPrefix = "# bd-shim "

// inlineHookMarker identifies inline hooks created by bd init (GH#1120)
// These hooks have the logic embedded directly rather than using shims
const inlineHookMarker = "# bd (beads)"

// Section markers for git hooks (GH#1380) — consistent with AGENTS.md pattern.
// Only content between markers is managed by beads; user content outside is preserved.
const hookSectionBeginPrefix = "# --- BEGIN BEADS INTEGRATION"
const hookSectionEndPrefix = "# --- END BEADS INTEGRATION"

// hookSectionBeginLine returns the full begin marker line with the current version.
func hookSectionBeginLine() string {
	return fmt.Sprintf("%s v%s ---", hookSectionBeginPrefix, Version)
}

// hookSectionEndLine returns the full end marker line with the current version.
func hookSectionEndLine() string {
	return fmt.Sprintf("%s v%s ---", hookSectionEndPrefix, Version)
}

// hookTimeoutSeconds is the maximum time a beads hook is allowed to run before
// being killed and allowing the git operation to proceed.  A bounded timeout
// prevents `bd hooks run` from hanging `git push` indefinitely (GH#2453).
// The default is 300 seconds (5 minutes) to accommodate chained hooks — e.g.
// pre-commit framework pipelines that run linters, type-checkers, and builds
// inside `bd hooks run` via the `.old` hook chain (GH#2732).
// The value can be overridden via the BEADS_HOOK_TIMEOUT environment variable.
const hookTimeoutSeconds = 300

// generateHookSection returns the marked section content for a given hook name.
// The section is self-contained: it checks for bd availability, runs the hook
// via 'bd hooks run', and propagates exit codes — without preventing any user
// content after the section from executing on success.
//
// Resilience (GH#2453, GH#2449):
//   - A configurable timeout prevents hooks from hanging git operations.
//   - If the beads database is not initialized (exit code 3), the hook exits
//     successfully with a warning so that git operations are not blocked.
func generateHookSection(hookName string) string {
	return hookSectionBeginLine() + "\n" +
		"# This section is managed by beads. Do not remove these markers.\n" +
		"if command -v bd >/dev/null 2>&1; then\n" +
		"  export BD_GIT_HOOK=1\n" +
		"  _bd_timeout=${BEADS_HOOK_TIMEOUT:-" + fmt.Sprintf("%d", hookTimeoutSeconds) + "}\n" +
		"  if command -v timeout >/dev/null 2>&1; then\n" +
		"    timeout \"$_bd_timeout\" bd hooks run " + hookName + " \"$@\"\n" +
		"    _bd_exit=$?\n" +
		"    if [ $_bd_exit -eq 124 ]; then\n" +
		"      echo >&2 \"beads: hook '" + hookName + "' timed out after ${_bd_timeout}s — continuing without beads\"\n" +
		"      _bd_exit=0\n" +
		"    fi\n" +
		"  else\n" +
		"    bd hooks run " + hookName + " \"$@\"\n" +
		"    _bd_exit=$?\n" +
		"  fi\n" +
		"  if [ $_bd_exit -eq 3 ]; then\n" +
		"    echo >&2 \"beads: database not initialized — skipping hook '" + hookName + "'\"\n" +
		"    _bd_exit=0\n" +
		"  fi\n" +
		"  if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\n" +
		"fi\n" +
		hookSectionEndLine() + "\n"
}

// injectHookSection merges the beads section into existing hook file content.
// If section markers are found, only the content between them is replaced.
// If broken markers exist (orphaned BEGIN, reversed order), the stale markers
// are removed before injecting the new section.
// If no markers are found, the section is appended.
func injectHookSection(existing, section string) string {
	return injectHookSectionWithDepth(existing, section, 0)
}

// maxInjectDepth guards against infinite recursion when cleaning broken markers.
const maxInjectDepth = 5

func injectHookSectionWithDepth(existing, section string, depth int) string {
	if depth > maxInjectDepth {
		// Safety: too many recursive cleanups — append as fallback
		result := existing
		if !strings.HasSuffix(result, "\n") {
			result += "\n"
		}
		return result + "\n" + section
	}

	beginIdx := strings.Index(existing, hookSectionBeginPrefix)
	endIdx := strings.Index(existing, hookSectionEndPrefix)

	if beginIdx != -1 && endIdx != -1 && beginIdx < endIdx {
		// Case 1: valid BEGIN...END pair — replace between markers
		lineStart := strings.LastIndex(existing[:beginIdx], "\n")
		if lineStart == -1 {
			lineStart = 0
		} else {
			lineStart++ // skip the newline itself
		}

		// Find end of the end-marker line (including trailing newline)
		endOfEndMarker := endIdx + len(hookSectionEndPrefix)
		// Consume the rest of the end-marker line (e.g. " v0.58.0 ---\n")
		restAfterPrefix := existing[endOfEndMarker:]
		if nlIdx := strings.Index(restAfterPrefix, "\n"); nlIdx != -1 {
			endOfEndMarker += nlIdx + 1
		} else {
			endOfEndMarker = len(existing)
		}

		return existing[:lineStart] + section + existing[endOfEndMarker:]
	} else if beginIdx != -1 {
		// Case 2: broken markers — orphaned BEGIN (no END) or reversed (END before BEGIN).
		// Remove the orphaned/stale block, then recurse to handle remaining markers.
		cleaned := removeOrphanedBeginBlock(existing, beginIdx)
		return injectHookSectionWithDepth(cleaned, section, depth+1)
	} else if endIdx != -1 {
		// Case 2b: orphaned END without BEGIN — remove the stale END line
		cleaned := removeMarkerLine(existing, endIdx, hookSectionEndPrefix)
		return injectHookSectionWithDepth(cleaned, section, depth+1)
	}

	// Case 3: no markers — append
	result := existing
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	result += "\n" + section
	return result
}

// removeOrphanedBeginBlock removes an orphaned BEGIN block starting at beginIdx.
// Scans forward from the BEGIN line to the next blank line, next BEGIN marker, or EOF.
func removeOrphanedBeginBlock(content string, beginIdx int) string {
	lineStart := strings.LastIndex(content[:beginIdx], "\n")
	if lineStart == -1 {
		lineStart = 0
	} else {
		lineStart++ // skip the newline itself
	}

	afterBegin := content[beginIdx:]
	blockEnd := len(content)

	lines := strings.SplitAfter(afterBegin, "\n")
	scanned := beginIdx
	for i, line := range lines {
		if i == 0 {
			// Skip the BEGIN line itself
			scanned += len(line)
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// Blank line — end of orphaned block (include the blank line)
			blockEnd = scanned + len(line)
			break
		}
		if strings.Contains(line, hookSectionBeginPrefix) {
			// Next BEGIN marker — end before this line
			blockEnd = scanned
			break
		}
		scanned += len(line)
	}

	return content[:lineStart] + content[blockEnd:]
}

// removeMarkerLine removes a single marker line from content.
func removeMarkerLine(content string, markerIdx int, markerPrefix string) string {
	lineStart := strings.LastIndex(content[:markerIdx], "\n")
	if lineStart == -1 {
		lineStart = 0
	} else {
		lineStart++ // skip the newline itself
	}

	lineEnd := markerIdx + len(markerPrefix)
	restAfterPrefix := content[lineEnd:]
	if nlIdx := strings.Index(restAfterPrefix, "\n"); nlIdx != -1 {
		lineEnd += nlIdx + 1
	} else {
		lineEnd = len(content)
	}

	return content[:lineStart] + content[lineEnd:]
}

// removeHookSection removes only the beads section from hook file content.
// Returns the content with the section removed, and true if a section was found.
// Handles valid BEGIN...END pairs, orphaned BEGIN, orphaned END, and reversed markers.
func removeHookSection(content string) (string, bool) {
	beginIdx := strings.Index(content, hookSectionBeginPrefix)
	endIdx := strings.Index(content, hookSectionEndPrefix)

	if beginIdx == -1 && endIdx == -1 {
		return content, false
	}

	if beginIdx != -1 && endIdx != -1 && beginIdx < endIdx {
		// Valid BEGIN...END pair — remove the whole section
		lineStart := strings.LastIndex(content[:beginIdx], "\n")
		if lineStart == -1 {
			lineStart = 0
		} else {
			lineStart++
		}

		endOfSection := endIdx + len(hookSectionEndPrefix)
		restAfterPrefix := content[endOfSection:]
		if nlIdx := strings.Index(restAfterPrefix, "\n"); nlIdx != -1 {
			endOfSection += nlIdx + 1
		} else {
			endOfSection = len(content)
		}

		// Also consume a blank line before the section if present
		if lineStart >= 2 && content[lineStart-1] == '\n' && content[lineStart-2] == '\n' {
			lineStart--
		}

		return content[:lineStart] + content[endOfSection:], true
	}

	// Broken markers: orphaned BEGIN, orphaned END, or reversed order.
	// Remove whichever markers exist.
	result := content
	if beginIdx != -1 {
		result = removeOrphanedBeginBlock(result, strings.Index(result, hookSectionBeginPrefix))
	}
	if endIdx != -1 {
		// Re-find END index in the (possibly modified) result
		if newEndIdx := strings.Index(result, hookSectionEndPrefix); newEndIdx != -1 {
			result = removeMarkerLine(result, newEndIdx, hookSectionEndPrefix)
		}
	}

	// Trim trailing blank lines that may be left from removal
	for strings.HasSuffix(result, "\n\n\n") {
		result = result[:len(result)-1]
	}

	return result, true
}

// HookStatus represents the status of a single git hook
type HookStatus struct {
	Name      string
	Installed bool
	Version   string
	IsShim    bool // true if this is a thin shim (version-agnostic)
	Outdated  bool
}

// CheckGitHooks checks the status of bd git hooks in .git/hooks/
func CheckGitHooks() []HookStatus {
	hooks := []string{"pre-commit", "post-merge", "pre-push", "post-checkout", "prepare-commit-msg"}
	statuses := make([]HookStatus, 0, len(hooks))

	// Get hooks directory from common git dir (hooks are shared across worktrees)
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		// Not a git repo - return all hooks as not installed
		for _, hookName := range hooks {
			statuses = append(statuses, HookStatus{Name: hookName, Installed: false})
		}
		return statuses
	}

	for _, hookName := range hooks {
		status := HookStatus{
			Name: hookName,
		}

		// Check if hook exists
		hookPath := filepath.Join(hooksDir, hookName)
		versionInfo, err := getHookVersion(hookPath)
		if err != nil {
			// Hook doesn't exist or couldn't be read
			status.Installed = false
		} else {
			status.Installed = true
			status.Version = versionInfo.Version
			status.IsShim = versionInfo.IsShim

			// Thin shims are never outdated (they delegate to bd)
			// bd hooks are outdated if version is missing (legacy inline) or differs
			if !versionInfo.IsShim && versionInfo.IsBdHook && versionInfo.Version != Version {
				status.Outdated = true
			}
		}

		statuses = append(statuses, status)
	}

	return statuses
}

// hookVersionInfo contains version information extracted from a hook file
type hookVersionInfo struct {
	Version  string // bd version (for legacy hooks) or shim version
	IsShim   bool   // true if this is a thin shim
	IsBdHook bool   // true if this is any type of bd hook (shim or inline)
}

// getHookVersion extracts the version from a hook file
func getHookVersion(path string) (hookVersionInfo, error) {
	// #nosec G304 -- hook path constrained to .git/hooks directory
	file, err := os.Open(path)
	if err != nil {
		return hookVersionInfo{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Read the entire file to support section markers anywhere (GH#1380)
	var content strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		content.WriteString(line)
		content.WriteString("\n")
		// Check for section marker (GH#1380) — can appear anywhere in the file
		if strings.HasPrefix(line, hookSectionBeginPrefix) {
			// Extract version from "# --- BEGIN BEADS INTEGRATION v0.56.1 ---"
			after := strings.TrimPrefix(line, hookSectionBeginPrefix)
			after = strings.TrimSpace(after)
			after = strings.TrimPrefix(after, "v")
			after = strings.TrimSuffix(after, "---")
			version := strings.TrimSpace(after)
			return hookVersionInfo{Version: version, IsShim: true, IsBdHook: true}, nil
		}
		// Check for thin shim marker first
		if strings.HasPrefix(line, shimVersionPrefix) {
			version := strings.TrimSpace(strings.TrimPrefix(line, shimVersionPrefix))
			return hookVersionInfo{Version: version, IsShim: true, IsBdHook: true}, nil
		}
		// Check for legacy version marker
		if strings.HasPrefix(line, hookVersionPrefix) {
			version := strings.TrimSpace(strings.TrimPrefix(line, hookVersionPrefix))
			return hookVersionInfo{Version: version, IsShim: false, IsBdHook: true}, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return hookVersionInfo{}, fmt.Errorf("reading hook file: %w", err)
	}

	// Check if it's an inline bd hook (from bd init) - GH#1120
	// These don't have version markers but have "# bd (beads)" comment
	if strings.Contains(content.String(), inlineHookMarker) {
		return hookVersionInfo{IsBdHook: true}, nil
	}

	// No version found and not a bd hook
	return hookVersionInfo{}, nil
}

// FormatHookWarnings returns a formatted warning message if hooks are outdated
func FormatHookWarnings(statuses []HookStatus) string {
	var warnings []string

	missingCount := 0
	outdatedCount := 0

	for _, status := range statuses {
		if !status.Installed {
			missingCount++
		} else if status.Outdated {
			outdatedCount++
		}
	}

	if missingCount > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️  Git hooks not installed (%d missing)", missingCount))
		warnings = append(warnings, "   Run: bd hooks install")
	}

	if outdatedCount > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️  Git hooks are outdated (%d hooks)", outdatedCount))
		warnings = append(warnings, "   Run: bd hooks install")
	}

	if len(warnings) > 0 {
		return strings.Join(warnings, "\n")
	}

	return ""
}

// Cobra commands

var hooksCmd = &cobra.Command{
	Use:     "hooks",
	GroupID: "setup",
	Short:   "Manage git hooks for beads integration",
	Long: `Install, uninstall, or list git hooks for beads integration.

The hooks provide:
- pre-commit: Run chained hooks before commit
- post-merge: Run chained hooks after pull/merge
- pre-push: Run chained hooks before push
- post-checkout: Run chained hooks after branch checkout
- prepare-commit-msg: Add agent identity trailers for forensics`,
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install bd git hooks",
	Long: `Install git hooks for beads integration.

By default, hooks are installed to .git/hooks/ in the current repository.
Use --beads to install to .beads/hooks/ (recommended for Dolt backend).
Use --shared to install to a versioned directory (.beads-hooks/) that can be
committed to git and shared with team members.

Hooks use section markers to coexist with existing hooks — any user content
outside the markers is preserved across installs and upgrades.

Installed hooks:
  - pre-commit: Run chained hooks before commit
  - post-merge: Run chained hooks after pull/merge
  - pre-push: Run chained hooks before push
  - post-checkout: Run chained hooks after branch checkout
  - prepare-commit-msg: Add agent identity trailers (for orchestrator agents)`,
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")
		shared, _ := cmd.Flags().GetBool("shared")
		chain, _ := cmd.Flags().GetBool("chain")
		beadsHooks, _ := cmd.Flags().GetBool("beads")

		if err := installHooksWithOptions(managedHookNames, force, shared, chain, beadsHooks); err != nil {
			FatalErrorRespectJSON("installing hooks: %v", err)
		}

		if jsonOutput {
			output := map[string]interface{}{
				"success":    true,
				"message":    "Git hooks installed successfully",
				"shared":     shared,
				"chained":    chain,
				"beadsHooks": beadsHooks,
			}
			jsonBytes, _ := json.MarshalIndent(output, "", "  ")
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Println("✓ Git hooks installed successfully")
			fmt.Println()
			if beadsHooks {
				fmt.Println("Hooks installed to: .beads/hooks/")
				fmt.Println("Git config set: core.hooksPath=.beads/hooks")
				fmt.Println()
			} else if shared {
				fmt.Println("Hooks installed to: .beads-hooks/")
				fmt.Println("Git config set: core.hooksPath=.beads-hooks")
				fmt.Println()
				fmt.Println("⚠️  Remember to commit .beads-hooks/ to share with your team!")
				fmt.Println()
			}
			fmt.Println("Installed hooks:")
			for _, hookName := range managedHookNames {
				fmt.Printf("  - %s\n", hookName)
			}
		}
	},
}

var hooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall bd git hooks",
	Long:  `Remove bd git hooks from .git/hooks/ directory.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := uninstallHooks(); err != nil {
			FatalErrorRespectJSON("uninstalling hooks: %v", err)
		}

		if jsonOutput {
			output := map[string]interface{}{
				"success": true,
				"message": "Git hooks uninstalled successfully",
			}
			jsonBytes, _ := json.MarshalIndent(output, "", "  ")
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Println("✓ Git hooks uninstalled successfully")
		}
	},
}

var hooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed git hooks status",
	Long:  `Show the status of bd git hooks (installed, outdated, missing).`,
	Run: func(cmd *cobra.Command, args []string) {
		statuses := CheckGitHooks()

		if jsonOutput {
			output := map[string]interface{}{
				"hooks": statuses,
			}
			jsonBytes, _ := json.MarshalIndent(output, "", "  ")
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Println("Git hooks status:")
			for _, status := range statuses {
				if !status.Installed {
					fmt.Printf("  ✗ %s: not installed\n", status.Name)
				} else if status.IsShim {
					fmt.Printf("  ✓ %s: installed (shim %s)\n", status.Name, status.Version)
				} else if status.Outdated {
					fmt.Printf("  ⚠ %s: installed (version %s, current: %s) - outdated\n",
						status.Name, status.Version, Version)
				} else {
					fmt.Printf("  ✓ %s: installed (version %s)\n", status.Name, status.Version)
				}
			}
		}
	},
}

//nolint:unparam // force and chain kept for CLI flag compatibility; section markers make them no-ops
func installHooksWithOptions(hookNames []string, force bool, shared bool, chain bool, beadsHooks bool) error {
	var hooksDir string
	if beadsHooks {
		// Use .beads/hooks/ directory (preferred for Dolt backend)
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return fmt.Errorf("not in a beads workspace (no .beads directory found)")
		}
		hooksDir = filepath.Join(beadsDir, "hooks")
	} else if shared {
		// Use versioned directory for shared hooks
		hooksDir = ".beads-hooks"
	} else {
		// Use common git directory for hooks (shared across worktrees)
		var err error
		hooksDir, err = git.GetGitHooksDir()
		if err != nil {
			return err
		}
	}

	// Create hooks directory if it doesn't exist
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// When setting a local core.hooksPath (beads or shared mode), preserve any
	// hooks from the previously effective hooks directory (e.g. a global
	// core.hooksPath or the default .git/hooks). Without this, setting a local
	// core.hooksPath silently shadows the global one and those hooks stop running.
	if beadsHooks || shared {
		preservePreexistingHooks(hooksDir)
	}

	// Install each hook using section markers (GH#1380).
	// Only the content between markers is managed by beads; user content
	// outside the markers is preserved across reinstalls and upgrades.
	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		section := generateHookSection(hookName)

		// Read existing hook file (if any)
		// #nosec G304 -- hook path constrained to hooks directory
		existing, readErr := os.ReadFile(hookPath)

		if readErr != nil && !os.IsNotExist(readErr) {
			return fmt.Errorf("failed to read %s: %w", hookName, readErr)
		}

		var newContent string
		if os.IsNotExist(readErr) {
			// No existing file — create with shebang + section
			newContent = "#!/usr/bin/env sh\n" + section
		} else {
			existingStr := string(existing)
			// Check if file already has section markers
			if strings.Contains(existingStr, hookSectionBeginPrefix) {
				// Update only the section between markers
				newContent = injectHookSection(existingStr, section)
			} else {
				// Check if this is a legacy bd hook (shim or inline)
				versionInfo, _ := getHookVersion(hookPath)
				if versionInfo.IsBdHook {
					// Legacy bd hook — replace entire file with section format
					newContent = "#!/usr/bin/env sh\n" + section
				} else {
					// Non-bd hook — inject section (preserving existing content)
					newContent = injectHookSection(existingStr, section)
				}
			}
		}

		// Normalize line endings to LF
		newContent = strings.ReplaceAll(newContent, "\r\n", "\n")

		// Write hook file
		// #nosec G306 -- git hooks must be executable for Git to run them
		if err := os.WriteFile(hookPath, []byte(newContent), 0755); err != nil {
			return fmt.Errorf("failed to write %s: %w", hookName, err)
		}
	}

	// Configure git to use the hooks directory
	if beadsHooks {
		if err := configureBeadsHooksPath(); err != nil {
			return fmt.Errorf("failed to configure git hooks path: %w", err)
		}
	} else if shared {
		if err := configureSharedHooksPath(); err != nil {
			return fmt.Errorf("failed to configure git hooks path: %w", err)
		}
	}

	return nil
}

// preservePreexistingHooks copies non-beads hooks from the currently effective
// hooks directory into targetDir. This prevents hooks from a global
// core.hooksPath (or the default .git/hooks/) from being silently lost when
// beads sets a local core.hooksPath override.
func preservePreexistingHooks(targetDir string) {
	// Get the hooks directory git would currently use (before we override it).
	currentDir, err := git.GetGitHooksDir()
	if err != nil {
		return
	}

	// Resolve to absolute paths for reliable comparison.
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return
	}
	absCurrent, err := filepath.Abs(currentDir)
	if err != nil {
		return
	}

	// If the current dir is already our target, this is a re-install — skip.
	if absTarget == absCurrent {
		return
	}

	// If current dir is already a beads-managed directory, skip.
	repoRoot := git.GetRepoRoot()
	if repoRoot != "" {
		absBeadsHooks, _ := filepath.Abs(filepath.Join(repoRoot, ".beads", "hooks"))
		absSharedHooks, _ := filepath.Abs(filepath.Join(repoRoot, ".beads-hooks"))
		if absCurrent == absBeadsHooks || absCurrent == absSharedHooks {
			return
		}
	}

	// Copy all hooks from the source directory, not just managed ones.
	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}

		srcPath := filepath.Join(currentDir, entry.Name())
		// #nosec G304 -- hook path constrained to known hooks directories
		content, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}

		contentStr := string(content)
		// Skip if it's already a beads hook
		if strings.Contains(contentStr, hookSectionBeginPrefix) ||
			strings.Contains(contentStr, inlineHookMarker) {
			continue
		}

		// Don't overwrite existing files in target
		dstPath := filepath.Join(targetDir, entry.Name())
		if _, err := os.Stat(dstPath); err == nil {
			continue
		}

		// #nosec G306 -- git hooks must be executable
		if err := os.WriteFile(dstPath, content, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to preserve %s hook from %s: %v\n", entry.Name(), currentDir, err)
			continue
		}
		fmt.Printf("  Preserving existing %s hook from %s\n", entry.Name(), currentDir)
	}
}

func configureSharedHooksPath() error {
	// Set git config core.hooksPath to an absolute path pointing to .beads-hooks.
	// Using an absolute path is critical for git worktrees (GH#2414):
	// git resolves relative core.hooksPath relative to the working tree root.
	// Note: This may run before .beads exists, so it uses git.GetRepoRoot() directly.
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	absHooksPath := filepath.Join(repoRoot, ".beads-hooks")
	cmd := exec.Command("git", "config", "core.hooksPath", absHooksPath)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config failed: %w (output: %s)", err, string(output))
	}
	return nil
}

func configureBeadsHooksPath() error {
	// Set git config core.hooksPath to an absolute path pointing to .beads/hooks.
	// Using an absolute path is critical for git worktrees (GH#2414):
	// git resolves relative core.hooksPath relative to the working tree root,
	// so in a worktree ".beads/hooks" would resolve to <worktree>/.beads/hooks/
	// which doesn't exist — the hooks live in the main repo's .beads/hooks/.
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	absHooksPath := filepath.Join(repoRoot, ".beads", "hooks")
	cmd := exec.Command("git", "config", "core.hooksPath", absHooksPath)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config failed: %w (output: %s)", err, string(output))
	}
	return nil
}

func uninstallHooks() error {
	// Get hooks directory from common git dir (hooks are shared across worktrees)
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return err
	}
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout", "prepare-commit-msg"}

	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)

		// #nosec G304 -- hook path constrained to .git/hooks directory
		content, err := os.ReadFile(hookPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("failed to read %s: %w", hookName, err)
		}

		// Try to remove only the beads section (GH#1380)
		newContent, found := removeHookSection(string(content))
		if found {
			remaining := strings.TrimSpace(newContent)
			if remaining == "" || remaining == "#!/usr/bin/env sh" || remaining == "#!/bin/sh" {
				// Only shebang left — remove the file entirely
				if err := os.Remove(hookPath); err != nil {
					return fmt.Errorf("failed to remove %s: %w", hookName, err)
				}
			} else {
				// #nosec G306 -- git hooks must be executable
				if err := os.WriteFile(hookPath, []byte(newContent), 0755); err != nil {
					return fmt.Errorf("failed to write %s: %w", hookName, err)
				}
			}
			continue
		}

		// No section markers — check if it's a legacy bd hook (remove entire file)
		versionInfo, verr := getHookVersion(hookPath)
		if verr == nil && versionInfo.IsBdHook {
			if err := os.Remove(hookPath); err != nil {
				return fmt.Errorf("failed to remove %s: %w", hookName, err)
			}
			// Restore backup if exists
			backupPath := hookPath + ".backup"
			if _, err := os.Stat(backupPath); err == nil {
				if err := os.Rename(backupPath, hookPath); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to restore backup for %s: %v\n", hookName, err)
				}
			}
		}
		// Not a bd hook at all — leave it alone
	}

	// Reset core.hooksPath if it was set to a beads-managed directory
	if err := resetHooksPathIfBeadsManaged(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to reset core.hooksPath: %v\n", err)
	}

	return nil
}

// resetHooksPathIfBeadsManaged unsets core.hooksPath if it points to a
// beads-managed hooks directory (.beads/hooks or .beads-hooks).
func resetHooksPathIfBeadsManaged() error {
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return nil // not in a git repo
	}

	cmd := exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil // core.hooksPath not set — nothing to reset
	}

	hooksPath := strings.TrimSpace(string(out))
	// Match both relative (legacy) and absolute (GH#2414) beads hooks paths
	absBeadsHooks := filepath.Join(repoRoot, ".beads", "hooks")
	absSharedHooks := filepath.Join(repoRoot, ".beads-hooks")
	if hooksPath == ".beads/hooks" || hooksPath == ".beads-hooks" ||
		hooksPath == absBeadsHooks || hooksPath == absSharedHooks {
		cmd = exec.Command("git", "config", "--unset", "core.hooksPath")
		cmd.Dir = repoRoot
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config --unset core.hooksPath failed: %w (output: %s)", err, string(output))
		}
	}

	return nil
}

// =============================================================================
// Hook Implementation Functions (called by thin shims via 'bd hooks run')
// =============================================================================

// runChainedHook runs a .old hook if it exists. Returns the exit code.
// If the hook doesn't exist, returns 0 (success).
func runChainedHook(hookName string, args []string) int {
	// Get the hooks directory from common dir (hooks are shared across worktrees)
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return 0 // Not a git repo, nothing to chain
	}

	oldHookPath := filepath.Join(hooksDir, hookName+".old")

	// Check if the .old hook exists and is executable
	info, err := os.Stat(oldHookPath)
	if err != nil {
		return 0 // No chained hook
	}
	if info.Mode().Perm()&0111 == 0 {
		return 0 // Not executable
	}

	// Check if .old is itself a bd hook (shim or inline) - skip to prevent infinite recursion
	// This can happen if user runs `bd hooks install --chain` multiple times,
	// renaming an existing bd hook to .old. See: GH#843, GH#1120
	versionInfo, err := getHookVersion(oldHookPath)
	if err == nil && versionInfo.IsBdHook {
		// Skip execution - .old is a bd hook which would call us again
		return 0
	}

	// Run the chained hook
	// #nosec G204 -- hookName is from controlled list, path is from git directory
	cmd := exec.Command(oldHookPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		// Other error - treat as failure
		fmt.Fprintf(os.Stderr, "Warning: chained hook %s failed: %v\n", hookName, err)
		return 1
	}

	return 0
}

// runPreCommitHook runs chained hooks before commit.
// Returns 0 on success (or if not applicable).
func runPreCommitHook() int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("pre-commit", nil); exitCode != 0 {
		return exitCode
	}

	// GH#2489, GH#1863: Export JSONL before commit so issue state lands in
	// the same commit as code changes.  maybeAutoExport() skips when
	// BD_GIT_HOOK=1, so we invoke `bd export` as a subprocess instead.
	exportJSONLForCommit()

	return 0
}

// exportJSONLForCommit exports Dolt issue state to the git-tracked JSONL file
// when export.auto is enabled. Called from the pre-commit hook so that the
// exported file can be staged and included in the pending commit.
//
// Errors are logged as warnings but never block the commit.
func exportJSONLForCommit() {
	if !config.GetBool("export.auto") {
		return
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	exportPath := config.GetString("export.path")
	if exportPath == "" {
		exportPath = "export.jsonl"
	}
	fullPath := filepath.Join(beadsDir, exportPath)

	debug.Logf("pre-commit: exporting JSONL to %s\n", fullPath)

	// Shell out to `bd export` which initializes its own store.
	// Clear BD_GIT_HOOK from the subprocess env so that its
	// PersistentPostRun auto-export path does not also fire.
	cmd := exec.Command("bd", "export", "-o", fullPath)
	cmd.Dir = beadsDir
	cmd.Env = filterEnv(os.Environ(), "BD_GIT_HOOK")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "beads: pre-commit export warning: %v\n", err)
		return
	}

	// Stage the exported file if configured.
	if config.GetBool("export.git-add") {
		addCmd := exec.Command("git", "add", fullPath)
		addCmd.Dir = filepath.Dir(fullPath)
		if err := addCmd.Run(); err != nil {
			debug.Logf("pre-commit: git add failed: %v\n", err)
		}
	}
}

// filterEnv returns a copy of env with entries matching the given key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// runPostMergeHook runs chained hooks after merge.
// Returns 0 on success (or if not applicable).
//
//nolint:unparam // Always returns 0 by design - warnings don't block merges
func runPostMergeHook() int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("post-merge", nil); exitCode != 0 {
		return exitCode
	}
	return 0
}

// runPrePushHook runs chained hooks before push.
// Returns 0 to allow push, non-zero to block.
func runPrePushHook(args []string) int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("pre-push", args); exitCode != 0 {
		return exitCode
	}
	return 0
}

// runPostCheckoutHook runs chained hooks after branch checkout.
// args: [previous-HEAD, new-HEAD, flag] where flag=1 for branch checkout
// Returns 0 on success (or if not applicable).
//
//nolint:unparam // Always returns 0 by design - warnings don't block checkouts
func runPostCheckoutHook(args []string) int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("post-checkout", args); exitCode != 0 {
		return exitCode
	}
	return 0
}

// runPrepareCommitMsgHook adds agent identity trailers to commit messages.
// args: [commit-msg-file, source, sha1]
// Returns 0 on success (or if not applicable), non-zero on error.
//
//nolint:unparam // Always returns 0 by design - we don't block commits
func runPrepareCommitMsgHook(args []string) int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("prepare-commit-msg", args); exitCode != 0 {
		return exitCode
	}

	if len(args) < 1 {
		return 0 // No message file provided
	}

	msgFile := args[0]
	source := ""
	if len(args) >= 2 {
		source = args[1]
	}

	// Skip for merge commits (they already have their own format)
	if source == "merge" {
		return 0
	}

	// Detect actor context from BD_ACTOR env var
	actor := os.Getenv("BD_ACTOR")
	if actor == "" {
		return 0 // Not in agent context, nothing to add
	}

	// Read current message
	content, err := os.ReadFile(msgFile) // #nosec G304 -- path from git
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read commit message: %v\n", err)
		return 0
	}

	// Check if trailer already present (avoid duplicates on amend)
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "Executed-By:") {
			return 0
		}
	}

	// Append Executed-By trailer
	msg := strings.TrimRight(string(content), "\n\r\t ")
	var sb strings.Builder
	sb.WriteString(msg)
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("Executed-By: %s\n", actor))

	// Write back
	if err := os.WriteFile(msgFile, []byte(sb.String()), 0600); err != nil { // Restrict permissions per gosec G306
		fmt.Fprintf(os.Stderr, "Warning: could not write commit message: %v\n", err)
	}

	return 0
}

// =============================================================================
// Hook Helper Functions
// =============================================================================

// isRebaseInProgress checks if a rebase is in progress.
func isRebaseInProgress() bool {
	if _, err := os.Stat(".git/rebase-merge"); err == nil {
		return true
	}
	if _, err := os.Stat(".git/rebase-apply"); err == nil {
		return true
	}
	return false
}

var hooksRunCmd = &cobra.Command{
	Use:   "run <hook-name> [args...]",
	Short: "Execute a git hook (called by thin shims)",
	Long: `Execute the logic for a git hook. This command is typically called by
thin shim scripts installed in .git/hooks/.

Supported hooks:
  - pre-commit: Run chained hooks before commit
  - post-merge: Run chained hooks after pull/merge
  - pre-push: Run chained hooks before push
  - post-checkout: Run chained hooks after branch checkout
  - prepare-commit-msg: Add agent identity trailers for forensics

The thin shim pattern ensures hook logic is always in sync with the
installed bd version - upgrading bd automatically updates hook behavior.`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// Disable terminal color probing to prevent OSC 11 escape sequence leaks (GH#1303).
		// Our shell shims set BD_GIT_HOOK=1 before invoking bd, but third-party hook
		// runners (lefthook, husky, etc.) call 'bd hooks run' directly without it.
		// By this point ui.init() has already run, so we must also reset styles
		// to suppress ANSI output — the env var alone only helps if set before process start.
		_ = os.Setenv("BD_GIT_HOOK", "1")
		ui.DisableColors()

		hookName := args[0]
		hookArgs := args[1:]

		var exitCode int
		switch hookName {
		case "pre-commit":
			exitCode = runPreCommitHook()
		case "post-merge":
			exitCode = runPostMergeHook()
		case "pre-push":
			exitCode = runPrePushHook(hookArgs)
		case "post-checkout":
			exitCode = runPostCheckoutHook(hookArgs)
		case "prepare-commit-msg":
			exitCode = runPrepareCommitMsgHook(hookArgs)
		default:
			FatalError("unknown hook: %s", hookName)
		}

		os.Exit(exitCode)
	},
}

func init() {
	hooksInstallCmd.Flags().Bool("force", false, "Overwrite existing hooks without backup")
	hooksInstallCmd.Flags().Bool("shared", false, "Install hooks to .beads-hooks/ (versioned) instead of .git/hooks/")
	hooksInstallCmd.Flags().Bool("chain", false, "Chain with existing hooks (run them before bd hooks)")
	hooksInstallCmd.Flags().Bool("beads", false, "Install hooks to .beads/hooks/ (recommended for Dolt backend)")

	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	hooksCmd.AddCommand(hooksListCmd)
	hooksCmd.AddCommand(hooksRunCmd)

	rootCmd.AddCommand(hooksCmd)
}
