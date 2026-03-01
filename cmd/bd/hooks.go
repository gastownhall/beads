package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

//go:embed templates/hooks/*
var hooksFS embed.FS

func getEmbeddedHooks() (map[string]string, error) {
	hooks := make(map[string]string)
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout", "prepare-commit-msg"}

	for _, name := range hookNames {
		content, err := hooksFS.ReadFile("templates/hooks/" + name)
		if err != nil {
			return nil, fmt.Errorf("failed to read embedded hook %s: %w", name, err)
		}
		// Normalize line endings to LF — embedded templates may contain CRLF
		// when built on Windows or from an NTFS-mounted filesystem (e.g. WSL).
		// Git hooks with CRLF fail: /usr/bin/env: 'sh\r': No such file or directory
		hooks[name] = strings.ReplaceAll(string(content), "\r\n", "\n")
	}

	return hooks, nil
}

const hookVersionPrefix = "# bd-hooks-version: "
const shimVersionPrefix = "# bd-shim "

// inlineHookMarker identifies inline hooks created by bd init (GH#1120)
// These hooks have the logic embedded directly rather than using shims
const inlineHookMarker = "# bd (beads)"

// Section markers for git hooks (GH#1380) — consistent with AGENTS.md pattern.
// Only content between markers is managed by beads; user content outside is preserved.
const hookSectionBeginPrefix = "# --- BEGIN BEADS INTEGRATION"
const hookSectionEnd = "# --- END BEADS INTEGRATION ---"

// hookSectionBeginLine returns the full begin marker line with the current version.
func hookSectionBeginLine() string {
	return fmt.Sprintf("%s v%s ---", hookSectionBeginPrefix, Version)
}

// generateHookSection returns the marked section content for a given hook name.
// The section is self-contained: it checks for bd availability, runs the hook
// via 'bd hooks run', and propagates exit codes — without preventing any user
// content after the section from executing on success.
func generateHookSection(hookName string) string {
	return hookSectionBeginLine() + "\n" +
		"# This section is managed by beads. Do not remove these markers.\n" +
		"if command -v bd >/dev/null 2>&1; then\n" +
		"  export BD_GIT_HOOK=1\n" +
		"  bd hooks run " + hookName + " \"$@\"\n" +
		"  _bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\n" +
		"fi\n" +
		hookSectionEnd + "\n"
}

// injectHookSection merges the beads section into existing hook file content.
// If section markers are found, only the content between them is replaced.
// If no markers are found, the section is appended.
func injectHookSection(existing, section string) string {
	beginIdx := strings.Index(existing, hookSectionBeginPrefix)
	endIdx := strings.Index(existing, hookSectionEnd)

	if beginIdx != -1 && endIdx != -1 && beginIdx < endIdx {
		// Find start of the begin-marker line
		lineStart := strings.LastIndex(existing[:beginIdx], "\n")
		if lineStart == -1 {
			lineStart = 0
		} else {
			lineStart++ // skip the newline itself
		}

		// Find end of the end-marker line (including trailing newline)
		endOfEndMarker := endIdx + len(hookSectionEnd)
		if endOfEndMarker < len(existing) && existing[endOfEndMarker] == '\n' {
			endOfEndMarker++
		}

		return existing[:lineStart] + section + existing[endOfEndMarker:]
	}

	// No markers found — append
	result := existing
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	result += "\n" + section
	return result
}

// removeHookSection removes only the beads section from hook file content.
// Returns the content with the section removed, and true if a section was found.
func removeHookSection(content string) (string, bool) {
	beginIdx := strings.Index(content, hookSectionBeginPrefix)
	endIdx := strings.Index(content, hookSectionEnd)

	if beginIdx == -1 || endIdx == -1 || beginIdx > endIdx {
		return content, false
	}

	// Find start of the begin-marker line
	lineStart := strings.LastIndex(content[:beginIdx], "\n")
	if lineStart == -1 {
		lineStart = 0
	} else {
		lineStart++ // skip the newline itself
	}

	// Find end of the end-marker line
	endOfEndMarker := endIdx + len(hookSectionEnd)
	if endOfEndMarker < len(content) && content[endOfEndMarker] == '\n' {
		endOfEndMarker++
	}

	// Also consume a blank line before the section if present
	if lineStart >= 2 && content[lineStart-1] == '\n' && content[lineStart-2] == '\n' {
		lineStart--
	}

	return content[:lineStart] + content[endOfEndMarker:], true
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
	Short:   "Manage git hooks for bd auto-sync",
	Long: `Install, uninstall, or list git hooks that provide automatic bd sync.

The hooks ensure that:
- pre-commit: Syncs pending changes before commit
- post-merge: Syncs database after pull/merge
- pre-push: Validates database state before push
- post-checkout: Syncs database after branch checkout
- prepare-commit-msg: Adds agent identity trailers for forensics`,
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install bd git hooks",
	Long: `Install git hooks for automatic bd sync.

By default, hooks are installed to .git/hooks/ in the current repository.
Use --beads to install to .beads/hooks/ (recommended for Dolt backend).
Use --shared to install to a versioned directory (.beads-hooks/) that can be
committed to git and shared with team members.

Use --chain to preserve existing hooks and run them before bd hooks. This is
useful if you have pre-commit framework hooks or other custom hooks.

Installed hooks:
  - pre-commit: Sync changes before commit
  - post-merge: Sync database after pull/merge
  - pre-push: Validate database state before push
  - post-checkout: Sync database after branch checkout
  - prepare-commit-msg: Add agent identity trailers (for orchestrator agents)`,
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")
		shared, _ := cmd.Flags().GetBool("shared")
		chain, _ := cmd.Flags().GetBool("chain")
		beadsHooks, _ := cmd.Flags().GetBool("beads")

		embeddedHooks, err := getEmbeddedHooks()
		if err != nil {
			FatalErrorRespectJSON("loading hooks: %v", err)
		}

		if err := installHooksWithOptions(embeddedHooks, force, shared, chain, beadsHooks); err != nil {
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
			if chain {
				fmt.Println("Mode: chained (existing hooks renamed to .old and will run first)")
				fmt.Println()
			}
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
			for hookName := range embeddedHooks {
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

func installHooks(embeddedHooks map[string]string, force bool, shared bool, chain bool) error {
	return installHooksWithOptions(embeddedHooks, force, shared, chain, false)
}

func installHooksWithOptions(embeddedHooks map[string]string, force bool, shared bool, chain bool, beadsHooks bool) error {
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

	// Install each hook using section markers (GH#1380).
	// Only the content between markers is managed by beads; user content
	// outside the markers is preserved across reinstalls and upgrades.
	for hookName := range embeddedHooks {
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

func configureSharedHooksPath() error {
	// Set git config core.hooksPath to .beads-hooks
	// Note: This may run before .beads exists, so it uses git.GetRepoRoot() directly
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	cmd := exec.Command("git", "config", "core.hooksPath", ".beads-hooks")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config failed: %w (output: %s)", err, string(output))
	}
	return nil
}

func configureBeadsHooksPath() error {
	// Set git config core.hooksPath to .beads/hooks
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	cmd := exec.Command("git", "config", "core.hooksPath", ".beads/hooks")
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
	return 0
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

	// Detect agent context
	identity := detectAgentIdentity()
	if identity == nil {
		return 0 // Not in agent context, nothing to add
	}

	// Read current message
	content, err := os.ReadFile(msgFile) // #nosec G304 -- path from git
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read commit message: %v\n", err)
		return 0
	}

	// Check if trailers already present (avoid duplicates on amend)
	// Look for "Executed-By:" at the start of a line (actual trailer format)
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "Executed-By:") {
			return 0
		}
	}

	// Build trailers
	var trailers []string
	trailers = append(trailers, fmt.Sprintf("Executed-By: %s", identity.FullIdentity))
	if identity.Rig != "" {
		trailers = append(trailers, fmt.Sprintf("Rig: %s", identity.Rig))
	}
	if identity.Role != "" {
		trailers = append(trailers, fmt.Sprintf("Role: %s", identity.Role))
	}
	if identity.Molecule != "" {
		trailers = append(trailers, fmt.Sprintf("Molecule: %s", identity.Molecule))
	}

	// Append trailers to message
	msg := strings.TrimRight(string(content), "\n\r\t ")
	var sb strings.Builder
	sb.WriteString(msg)
	sb.WriteString("\n\n")
	for _, trailer := range trailers {
		sb.WriteString(trailer)
		sb.WriteString("\n")
	}

	// Write back
	if err := os.WriteFile(msgFile, []byte(sb.String()), 0600); err != nil { // Restrict permissions per gosec G306
		fmt.Fprintf(os.Stderr, "Warning: could not write commit message: %v\n", err)
	}

	return 0
}

// agentIdentity holds detected agent context information.
type agentIdentity struct {
	FullIdentity string // e.g., "beads/crew/dave"
	Rig          string // e.g., "beads"
	Role         string // e.g., "crew"
	Molecule     string // e.g., "bd-xyz" (if attached)
}

// detectAgentIdentity returns agent identity if running in agent context.
// Returns nil if not in an agent context (human commit).
func detectAgentIdentity() *agentIdentity {
	// Check GT_ROLE environment variable first (set by orchestrator sessions)
	gtRole := os.Getenv("GT_ROLE")
	if gtRole != "" {
		return parseAgentIdentity(gtRole)
	}

	// Fall back to cwd-based detection
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	// Detect from path patterns
	return detectAgentFromPath(cwd)
}

// parseAgentIdentity parses a GT_ROLE value into agent identity.
// Only supports compound format (e.g., "beads/crew/dave").
// Simple format role names are Gas Town concepts and should be
// expanded to compound format by gastown before being set.
func parseAgentIdentity(role string) *agentIdentity {
	// Only support compound format: "beads/crew/dave", "gastown/polecats/Nux-123"
	// Simple formats like "crew" or "polecat" are Gas Town concepts -
	// gastown should expand them to compound format before setting GT_ROLE.
	if !strings.Contains(role, "/") {
		return nil
	}

	parts := strings.Split(role, "/")
	identity := &agentIdentity{FullIdentity: role}

	if len(parts) >= 1 {
		identity.Rig = parts[0]
	}
	if len(parts) >= 2 {
		identity.Role = parts[1]
	}

	// Check for molecule
	identity.Molecule = getPinnedMolecule()

	return identity
}

// detectAgentFromPath is deprecated - path-based agent detection is a
// Gas Town concept and should be handled by gastown, not beads.
// Returns nil - agents should set GT_ROLE in compound format instead.
func detectAgentFromPath(cwd string) *agentIdentity {
	return nil
}

// getPinnedMolecule checks if there's a molecule attached via gt mol status.
func getPinnedMolecule() string {
	// Try gt mol status --json
	cmd := exec.Command("gt", "mol", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Parse JSON response
	var status struct {
		HasMolecule bool   `json:"has_molecule"`
		MoleculeID  string `json:"molecule_id"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return ""
	}

	if status.HasMolecule && status.MoleculeID != "" {
		return status.MoleculeID
	}

	return ""
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
