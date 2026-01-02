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
		hooks[name] = string(content)
	}

	return hooks, nil
}

const hookVersionPrefix = "# bd-hooks-version: "
const shimVersionPrefix = "# bd-shim "

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

	// Get actual git directory (handles worktrees)
	gitDir, err := git.GetGitDir()
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
		hookPath := filepath.Join(gitDir, "hooks", hookName)
		versionInfo, err := getHookVersion(hookPath)
		if err != nil {
			// Hook doesn't exist or couldn't be read
			status.Installed = false
		} else {
			status.Installed = true
			status.Version = versionInfo.Version
			status.IsShim = versionInfo.IsShim

			// Thin shims are never outdated (they delegate to bd)
			// Legacy hooks are outdated if version differs from current bd version
			if !versionInfo.IsShim && versionInfo.Version != "" && versionInfo.Version != Version {
				status.Outdated = true
			}
		}

		statuses = append(statuses, status)
	}

	return statuses
}

// hookVersionInfo contains version information extracted from a hook file
type hookVersionInfo struct {
	Version string // bd version (for legacy hooks) or shim version
	IsShim  bool   // true if this is a thin shim
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
	// Read first few lines looking for version marker
	lineCount := 0
	for scanner.Scan() && lineCount < 10 {
		line := scanner.Text()
		// Check for thin shim marker first
		if strings.HasPrefix(line, shimVersionPrefix) {
			version := strings.TrimSpace(strings.TrimPrefix(line, shimVersionPrefix))
			return hookVersionInfo{Version: version, IsShim: true}, nil
		}
		// Check for legacy version marker
		if strings.HasPrefix(line, hookVersionPrefix) {
			version := strings.TrimSpace(strings.TrimPrefix(line, hookVersionPrefix))
			return hookVersionInfo{Version: version, IsShim: false}, nil
		}
		lineCount++
	}

	// No version found (old hook)
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
- pre-commit: Flushes pending changes to JSONL before commit
- post-merge: Imports updated JSONL after pull/merge
- pre-push: Prevents pushing stale JSONL
- post-checkout: Imports JSONL after branch checkout
- prepare-commit-msg: Adds agent identity trailers for forensics`,
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install bd git hooks",
	Long: `Install git hooks for automatic bd sync.

By default, hooks are installed to .git/hooks/ in the current repository.
Use --shared to install to a versioned directory (.beads-hooks/) that can be
committed to git and shared with team members.

Use --chain to preserve existing hooks and run them before bd hooks. This is
useful if you have pre-commit framework hooks or other custom hooks.

Installed hooks:
  - pre-commit: Flush changes to JSONL before commit
  - post-merge: Import JSONL after pull/merge
  - pre-push: Prevent pushing stale JSONL
  - post-checkout: Import JSONL after branch checkout
  - prepare-commit-msg: Add agent identity trailers (for orchestrator agents)`,
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")
		shared, _ := cmd.Flags().GetBool("shared")
		chain, _ := cmd.Flags().GetBool("chain")

		embeddedHooks, err := getEmbeddedHooks()
		if err != nil {
			if jsonOutput {
				output := map[string]interface{}{
					"error": err.Error(),
				}
				jsonBytes, _ := json.MarshalIndent(output, "", "  ")
				fmt.Println(string(jsonBytes))
			} else {
				fmt.Fprintf(os.Stderr, "Error loading hooks: %v\n", err)
			}
			os.Exit(1)
		}

		if err := installHooks(embeddedHooks, force, shared, chain); err != nil {
			if jsonOutput {
				output := map[string]interface{}{
					"error": err.Error(),
				}
				jsonBytes, _ := json.MarshalIndent(output, "", "  ")
				fmt.Println(string(jsonBytes))
			} else {
				fmt.Fprintf(os.Stderr, "Error installing hooks: %v\n", err)
			}
			os.Exit(1)
		}

		if jsonOutput {
			output := map[string]interface{}{
				"success": true,
				"message": "Git hooks installed successfully",
				"shared":  shared,
				"chained": chain,
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
			if shared {
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
			if jsonOutput {
				output := map[string]interface{}{
					"error": err.Error(),
				}
				jsonBytes, _ := json.MarshalIndent(output, "", "  ")
				fmt.Println(string(jsonBytes))
			} else {
				fmt.Fprintf(os.Stderr, "Error uninstalling hooks: %v\n", err)
			}
			os.Exit(1)
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
	// Get actual git directory (handles worktrees where .git is a file)
	gitDir, err := git.GetGitDir()
	if err != nil {
		return err
	}

	var hooksDir string
	if shared {
		// Use versioned directory for shared hooks
		hooksDir = ".beads-hooks"
	} else {
		// Use standard .git/hooks directory
		hooksDir = filepath.Join(gitDir, "hooks")
	}

	// Create hooks directory if it doesn't exist
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// Install each hook
	for hookName, hookContent := range embeddedHooks {
		hookPath := filepath.Join(hooksDir, hookName)

		// Check if hook already exists
		if _, err := os.Stat(hookPath); err == nil {
			if chain {
				// Chain mode - rename to .old so bd hooks run can call it
				// But skip if existing hook is already a bd shim - renaming it would
				// cause infinite recursion. See: https://github.com/steveyegge/beads/issues/843
				versionInfo, verr := getHookVersion(hookPath)
				if verr != nil || !versionInfo.IsShim {
					// Not a bd shim - safe to rename for chaining
					oldPath := hookPath + ".old"
					if err := os.Rename(hookPath, oldPath); err != nil {
						return fmt.Errorf("failed to rename %s to .old for chaining: %w", hookName, err)
					}
				}
				// If it IS a bd shim, just overwrite it (no rename needed)
			} else if !force {
				// Default mode - back it up
				backupPath := hookPath + ".backup"
				if err := os.Rename(hookPath, backupPath); err != nil {
					return fmt.Errorf("failed to backup %s: %w", hookName, err)
				}
			}
			// If force is set and not chaining, we just overwrite
		}

		// Write hook file
		// #nosec G306 -- git hooks must be executable for Git to run them
		if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
			return fmt.Errorf("failed to write %s: %w", hookName, err)
		}
	}

	// If shared mode, configure git to use the shared hooks directory
	if shared {
		if err := configureSharedHooksPath(); err != nil {
			return fmt.Errorf("failed to configure git hooks path: %w", err)
		}
	}

	return nil
}

func configureSharedHooksPath() error {
	// Set git config core.hooksPath to .beads-hooks
	cmd := exec.Command("git", "config", "core.hooksPath", ".beads-hooks")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config failed: %w (output: %s)", err, string(output))
	}
	return nil
}

func uninstallHooks() error {
	// Get actual git directory (handles worktrees)
	gitDir, err := git.GetGitDir()
	if err != nil {
		return err
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout", "prepare-commit-msg"}

	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)

		// Check if hook exists
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			continue
		}

		// Remove hook
		if err := os.Remove(hookPath); err != nil {
			return fmt.Errorf("failed to remove %s: %w", hookName, err)
		}

		// Restore backup if exists
		backupPath := hookPath + ".backup"
		if _, err := os.Stat(backupPath); err == nil {
			if err := os.Rename(backupPath, hookPath); err != nil {
				// Non-fatal - just warn
				fmt.Fprintf(os.Stderr, "Warning: failed to restore backup for %s: %v\n", hookName, err)
			}
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
	// Get the hooks directory
	gitDir, err := git.GetGitDir()
	if err != nil {
		return 0 // Not a git repo, nothing to chain
	}

	oldHookPath := filepath.Join(gitDir, "hooks", hookName+".old")

	// Check if the .old hook exists and is executable
	info, err := os.Stat(oldHookPath)
	if err != nil {
		return 0 // No chained hook
	}
	if info.Mode().Perm()&0111 == 0 {
		return 0 // Not executable
	}

	// Check if .old is itself a bd shim - skip to prevent infinite recursion
	// This can happen if user runs `bd hooks install --chain` multiple times,
	// renaming an existing bd shim to .old. See: https://github.com/steveyegge/beads/issues/843
	versionInfo, err := getHookVersion(oldHookPath)
	if err == nil && versionInfo.IsShim {
		// Skip execution - .old is a bd shim which would call us again
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

// runPreCommitHook flushes pending changes to JSONL before commit.
// Returns 0 on success (or if not applicable), 1 if unstaged beads changes detected.
func runPreCommitHook() int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("pre-commit", nil); exitCode != 0 {
		return exitCode
	}

	// Check if we're in a bd workspace
	if _, err := os.Stat(".beads"); os.IsNotExist(err) {
		return 0 // Not a bd workspace, nothing to do
	}

	// Check if sync-branch is configured (changes go to separate branch)
	if hookGetSyncBranch() != "" {
		return 0 // Skip - changes synced to separate branch
	}

	// Flush pending changes to JSONL
	// Use --flush-only to skip git operations (we're already in a git hook)
	cmd := exec.Command("bd", "sync", "--flush-only")
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: Failed to flush bd changes to JSONL")
		fmt.Fprintln(os.Stderr, "Run 'bd sync --flush-only' manually to diagnose")
		// Don't block the commit - user may have removed beads or have other issues
	}

	// Stage JSONL files for commit
	// By default, we auto-stage for convenience. Users with conflicting git hooks
	// (e.g., hooks that read the staging area) can set BEADS_NO_AUTO_STAGE=1 to
	// disable this and stage manually. See: https://github.com/steveyegge/beads/issues/826
	jsonlFiles := []string{".beads/beads.jsonl", ".beads/issues.jsonl", ".beads/deletions.jsonl", ".beads/interactions.jsonl"}

	if os.Getenv("BEADS_NO_AUTO_STAGE") != "" {
		// Safe mode: check for unstaged changes and block if found
		var unstaged []string
		for _, f := range jsonlFiles {
			if _, err := os.Stat(f); err == nil {
				if hasUnstagedChanges(f) {
					unstaged = append(unstaged, f)
				}
			}
		}

		if len(unstaged) > 0 {
			fmt.Fprintln(os.Stderr, "❌ Unstaged beads changes detected:")
			for _, f := range unstaged {
				fmt.Fprintf(os.Stderr, "   %s\n", f)
			}
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Run: git add .beads/")
			return 1
		}
	} else {
		// Default: auto-stage JSONL files
		for _, f := range jsonlFiles {
			if _, err := os.Stat(f); err == nil {
				// #nosec G204 - f is from hardcoded list above, not user input
				gitAdd := exec.Command("git", "add", f)
				_ = gitAdd.Run() // Ignore errors - file may not exist
			}
		}
	}

	return 0
}

// runPostMergeHook imports JSONL after pull/merge.
// Returns 0 on success (or if not applicable), non-zero on error.
//
//nolint:unparam // Always returns 0 by design - warnings don't block merges
func runPostMergeHook() int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("post-merge", nil); exitCode != 0 {
		return exitCode
	}

	// Skip during rebase
	if isRebaseInProgress() {
		return 0
	}

	// Check if we're in a bd workspace
	if _, err := os.Stat(".beads"); os.IsNotExist(err) {
		return 0
	}

	// Check if any JSONL file exists
	if !hasBeadsJSONL() {
		return 0
	}

	// Run bd sync --import-only --no-git-history
	cmd := exec.Command("bd", "sync", "--import-only", "--no-git-history")
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: Failed to sync bd changes after merge")
		fmt.Fprintln(os.Stderr, string(output))
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Run 'bd doctor --fix' to diagnose and repair")
		// Don't fail the merge, just warn
	}

	// Run quick health check
	healthCmd := exec.Command("bd", "doctor", "--check-health")
	_ = healthCmd.Run() // Ignore errors

	return 0
}

// runPrePushHook prevents pushing stale JSONL.
// Returns 0 to allow push, non-zero to block.
func runPrePushHook() int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("pre-push", nil); exitCode != 0 {
		return exitCode
	}

	// Check if we're in a bd workspace
	if _, err := os.Stat(".beads"); os.IsNotExist(err) {
		return 0
	}

	// Skip if bd sync is already in progress (prevents circular error)
	if os.Getenv("BD_SYNC_IN_PROGRESS") != "" {
		return 0
	}

	// Check if sync-branch is configured
	if hookGetSyncBranch() != "" {
		return 0 // Skip - changes synced to separate branch
	}

	// Flush pending bd changes
	flushCmd := exec.Command("bd", "sync", "--flush-only")
	_ = flushCmd.Run() // Ignore errors

	// Check for uncommitted JSONL changes
	files := []string{}
	for _, f := range []string{".beads/beads.jsonl", ".beads/issues.jsonl", ".beads/deletions.jsonl", ".beads/interactions.jsonl"} {
		// Check if file exists or is tracked
		if _, err := os.Stat(f); err == nil {
			files = append(files, f)
		} else {
			// Check if tracked by git
			// #nosec G204 - f is from hardcoded list above, not user input
			checkCmd := exec.Command("git", "ls-files", "--error-unmatch", f)
			if checkCmd.Run() == nil {
				files = append(files, f)
			}
		}
	}

	if len(files) == 0 {
		return 0
	}

	// Check for uncommitted changes using git status
	args := append([]string{"status", "--porcelain", "--"}, files...)
	// #nosec G204 - args built from hardcoded list and git subcommands
	statusCmd := exec.Command("git", args...)
	output, _ := statusCmd.Output()
	if len(output) > 0 {
		fmt.Fprintln(os.Stderr, "❌ Error: Uncommitted changes detected")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Before pushing, ensure all changes are committed. This includes:")
		fmt.Fprintln(os.Stderr, "  • bd JSONL updates (run 'bd sync')")
		fmt.Fprintln(os.Stderr, "  • any other modified files (run 'git status' to review)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Run 'bd sync' to commit these changes:")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  bd sync")
		fmt.Fprintln(os.Stderr, "")
		return 1
	}

	return 0
}

// runPostCheckoutHook imports JSONL after branch checkout.
// args: [previous-HEAD, new-HEAD, flag] where flag=1 for branch checkout
// Returns 0 on success (or if not applicable), non-zero on error.
//
//nolint:unparam // Always returns 0 by design - warnings don't block checkouts
func runPostCheckoutHook(args []string) int {
	// Run chained hook first (if exists)
	if exitCode := runChainedHook("post-checkout", args); exitCode != 0 {
		return exitCode
	}

	// Only run on branch checkouts (flag=1)
	if len(args) >= 3 && args[2] != "1" {
		return 0
	}

	// Skip during rebase
	if isRebaseInProgress() {
		return 0
	}

	// Check if we're in a bd workspace
	if _, err := os.Stat(".beads"); os.IsNotExist(err) {
		return 0
	}

	// Detect git worktree and show warning
	if isGitWorktree() {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════════════╗")
		fmt.Fprintln(os.Stderr, "║ Welcome to beads in git worktree!                                        ║")
		fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════════════╣")
		fmt.Fprintln(os.Stderr, "║ Note: Daemon mode is not recommended with git worktrees.                 ║")
		fmt.Fprintln(os.Stderr, "║ Worktrees share the same database, and the daemon may commit changes     ║")
		fmt.Fprintln(os.Stderr, "║ to the wrong branch.                                                     ║")
		fmt.Fprintln(os.Stderr, "║                                                                          ║")
		fmt.Fprintln(os.Stderr, "║ RECOMMENDED: Disable daemon for this session:                            ║")
		fmt.Fprintln(os.Stderr, "║   export BEADS_NO_DAEMON=1                                               ║")
		fmt.Fprintln(os.Stderr, "║                                                                          ║")
		fmt.Fprintln(os.Stderr, "║ For more information:                                                    ║")
		fmt.Fprintln(os.Stderr, "║   - Run: bd doctor                                                       ║")
		fmt.Fprintln(os.Stderr, "║   - Read: docs/GIT_INTEGRATION.md (lines 10-53)                          ║")
		fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════════════╝")
		fmt.Fprintln(os.Stderr, "")
	}

	// Check if any JSONL file exists
	if !hasBeadsJSONL() {
		return 0
	}

	// Run bd sync --import-only --no-git-history
	cmd := exec.Command("bd", "sync", "--import-only", "--no-git-history")
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: Failed to sync bd changes after checkout")
		fmt.Fprintln(os.Stderr, string(output))
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Run 'bd doctor --fix' to diagnose and repair")
		// Don't fail the checkout, just warn
	}

	// Run quick health check
	healthCmd := exec.Command("bd", "doctor", "--check-health")
	_ = healthCmd.Run() // Ignore errors

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
func parseAgentIdentity(role string) *agentIdentity {
	// GT_ROLE can be:
	// - Simple: "crew", "polecat", "witness", "refinery", "mayor"
	// - Compound: "beads/crew/dave", "gastown/polecat/Nux-123"

	if strings.Contains(role, "/") {
		// Compound format
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

	// Simple format - need to combine with env vars
	rig := os.Getenv("GT_RIG")
	identity := &agentIdentity{Role: role, Rig: rig}

	switch role {
	case "crew":
		crew := os.Getenv("GT_CREW")
		if rig != "" && crew != "" {
			identity.FullIdentity = fmt.Sprintf("%s/crew/%s", rig, crew)
		}
	case "polecat":
		polecat := os.Getenv("GT_POLECAT")
		if rig != "" && polecat != "" {
			identity.FullIdentity = fmt.Sprintf("%s/%s", rig, polecat)
		}
	case "witness":
		if rig != "" {
			identity.FullIdentity = fmt.Sprintf("%s/witness", rig)
		}
	case "refinery":
		if rig != "" {
			identity.FullIdentity = fmt.Sprintf("%s/refinery", rig)
		}
	case "mayor":
		identity.FullIdentity = "mayor"
		identity.Rig = "" // Mayor is rig-agnostic
	case "deacon":
		identity.FullIdentity = "deacon"
		identity.Rig = "" // Deacon is rig-agnostic
	}

	if identity.FullIdentity == "" {
		return nil
	}

	identity.Molecule = getPinnedMolecule()
	return identity
}

// detectAgentFromPath detects agent identity from cwd path patterns.
func detectAgentFromPath(cwd string) *agentIdentity {
	// Match patterns like:
	// - /Users/.../gt/<rig>/crew/<name>/...
	// - /Users/.../gt/<rig>/polecats/<name>/...
	// - /Users/.../gt/<rig>/witness/...
	// - /Users/.../gt/<rig>/refinery/...

	// Crew pattern
	if strings.Contains(cwd, "/crew/") {
		parts := strings.Split(cwd, "/crew/")
		if len(parts) >= 2 {
			rigPath := parts[0]
			crewPath := parts[1]
			rig := filepath.Base(rigPath)
			crew := strings.Split(crewPath, "/")[0]
			return &agentIdentity{
				FullIdentity: fmt.Sprintf("%s/crew/%s", rig, crew),
				Rig:          rig,
				Role:         "crew",
				Molecule:     getPinnedMolecule(),
			}
		}
	}

	// Polecat pattern
	if strings.Contains(cwd, "/polecats/") {
		parts := strings.Split(cwd, "/polecats/")
		if len(parts) >= 2 {
			rigPath := parts[0]
			polecatPath := parts[1]
			rig := filepath.Base(rigPath)
			polecat := strings.Split(polecatPath, "/")[0]
			return &agentIdentity{
				FullIdentity: fmt.Sprintf("%s/%s", rig, polecat),
				Rig:          rig,
				Role:         "polecat",
				Molecule:     getPinnedMolecule(),
			}
		}
	}

	// Witness pattern
	if strings.Contains(cwd, "/witness/") || strings.HasSuffix(cwd, "/witness") {
		parts := strings.Split(cwd, "/witness")
		if len(parts) >= 1 {
			rig := filepath.Base(parts[0])
			return &agentIdentity{
				FullIdentity: fmt.Sprintf("%s/witness", rig),
				Rig:          rig,
				Role:         "witness",
			}
		}
	}

	// Refinery pattern
	if strings.Contains(cwd, "/refinery/") || strings.HasSuffix(cwd, "/refinery") {
		parts := strings.Split(cwd, "/refinery")
		if len(parts) >= 1 {
			rig := filepath.Base(parts[0])
			return &agentIdentity{
				FullIdentity: fmt.Sprintf("%s/refinery", rig),
				Rig:          rig,
				Role:         "refinery",
			}
		}
	}

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

// hookGetSyncBranch returns the configured sync branch for hook context.
// This is a simplified version that doesn't require context.
func hookGetSyncBranch() string {
	// Check environment variable first
	if branch := os.Getenv("BEADS_SYNC_BRANCH"); branch != "" {
		return branch
	}

	// Check config.yaml
	configPath := ".beads/config.yaml"
	data, err := os.ReadFile(configPath) // #nosec G304 -- config path is hardcoded
	if err != nil {
		return ""
	}

	// Simple YAML parsing for sync-branch
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "sync-branch:") {
			value := strings.TrimPrefix(line, "sync-branch:")
			value = strings.TrimSpace(value)
			value = strings.Trim(value, `"'`)
			return value
		}
	}

	return ""
}

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

// hasBeadsJSONL checks if any JSONL file exists in .beads/.
func hasBeadsJSONL() bool {
	for _, f := range []string{".beads/beads.jsonl", ".beads/issues.jsonl", ".beads/deletions.jsonl", ".beads/interactions.jsonl"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// hasUnstagedChanges checks if a file has uncommitted changes (modified or untracked).
// Returns true if the file needs to be staged before commit.
func hasUnstagedChanges(path string) bool {
	// Check git status for this specific file
	// #nosec G204 - path is from hardcoded list in caller
	cmd := exec.Command("git", "status", "--porcelain", "--", path)
	output, err := cmd.Output()
	if err != nil {
		return false // If git fails, assume no changes
	}

	// Parse porcelain output: XY filename
	// X = staged status, Y = unstaged status
	// We care about Y (unstaged) being non-space, OR the file being untracked (??)
	status := strings.TrimSpace(string(output))
	if status == "" {
		return false // No changes
	}

	// Check each line (usually just one for a single file)
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		// Untracked file
		if x == '?' && y == '?' {
			return true
		}
		// Modified but not staged (Y is M, D, etc.)
		if y != ' ' {
			return true
		}
	}

	return false
}

var hooksRunCmd = &cobra.Command{
	Use:   "run <hook-name> [args...]",
	Short: "Execute a git hook (called by thin shims)",
	Long: `Execute the logic for a git hook. This command is typically called by
thin shim scripts installed in .git/hooks/.

Supported hooks:
  - pre-commit: Flush pending changes to JSONL before commit
  - post-merge: Import JSONL after pull/merge
  - pre-push: Prevent pushing stale JSONL
  - post-checkout: Import JSONL after branch checkout
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
			exitCode = runPrePushHook()
		case "post-checkout":
			exitCode = runPostCheckoutHook(hookArgs)
		case "prepare-commit-msg":
			exitCode = runPrepareCommitMsgHook(hookArgs)
		default:
			fmt.Fprintf(os.Stderr, "Unknown hook: %s\n", hookName)
			os.Exit(1)
		}

		os.Exit(exitCode)
	},
}

func init() {
	hooksInstallCmd.Flags().Bool("force", false, "Overwrite existing hooks without backup")
	hooksInstallCmd.Flags().Bool("shared", false, "Install hooks to .beads-hooks/ (versioned) instead of .git/hooks/")
	hooksInstallCmd.Flags().Bool("chain", false, "Chain with existing hooks (run them before bd hooks)")

	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	hooksCmd.AddCommand(hooksListCmd)
	hooksCmd.AddCommand(hooksRunCmd)

	rootCmd.AddCommand(hooksCmd)
}
