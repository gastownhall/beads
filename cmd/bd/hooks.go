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
)

// getGitDir returns the actual .git directory path.
// In a normal repo, this is ".git". In a worktree, .git is a file
// containing "gitdir: /path/to/actual/git/dir", so we use git rev-parse.
func getGitDir() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

//go:embed templates/hooks/*
var hooksFS embed.FS

func getEmbeddedHooks() (map[string]string, error) {
	hooks := make(map[string]string)
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}

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

// HookStatus represents the status of a single git hook
type HookStatus struct {
	Name      string
	Installed bool
	Version   string
	Outdated  bool
}

// CheckGitHooks checks the status of bd git hooks in .git/hooks/
func CheckGitHooks() []HookStatus {
	hooks := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}
	statuses := make([]HookStatus, 0, len(hooks))

	// Get actual git directory (handles worktrees)
	gitDir, err := getGitDir()
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
		version, err := getHookVersion(hookPath)
		if err != nil {
			// Hook doesn't exist or couldn't be read
			status.Installed = false
		} else {
			status.Installed = true
			status.Version = version

			// Check if outdated (compare to current bd version)
			if version != "" && version != Version {
				status.Outdated = true
			}
		}

		statuses = append(statuses, status)
	}

	return statuses
}

// getHookVersion extracts the version from a hook file
func getHookVersion(path string) (string, error) {
	// #nosec G304 -- hook path constrained to .git/hooks directory
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Read first few lines looking for version marker
	lineCount := 0
	for scanner.Scan() && lineCount < 10 {
		line := scanner.Text()
		if strings.HasPrefix(line, hookVersionPrefix) {
			version := strings.TrimSpace(strings.TrimPrefix(line, hookVersionPrefix))
			return version, nil
		}
		lineCount++
	}

	// No version found (old hook)
	return "", nil
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
	Use:   "hooks",
	Short: "Manage git hooks for bd auto-sync",
	Long: `Install, uninstall, or list git hooks that provide automatic bd sync.

The hooks ensure that:
- pre-commit: Flushes pending changes to JSONL before commit
- post-merge: Imports updated JSONL after pull/merge
- pre-push: Prevents pushing stale JSONL
- post-checkout: Imports JSONL after branch checkout`,
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install bd git hooks",
	Long: `Install git hooks for automatic bd sync.

By default, hooks are installed to .git/hooks/ in the current repository.
Use --shared to install to a versioned directory (.beads-hooks/) that can be
committed to git and shared with team members.

Installed hooks:
  - pre-commit: Flush changes to JSONL before commit
  - post-merge: Import JSONL after pull/merge
  - pre-push: Prevent pushing stale JSONL
  - post-checkout: Import JSONL after branch checkout`,
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")
		shared, _ := cmd.Flags().GetBool("shared")

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

		if err := installHooks(embeddedHooks, force, shared); err != nil {
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
			}
			jsonBytes, _ := json.MarshalIndent(output, "", "  ")
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Println("✓ Git hooks installed successfully")
			fmt.Println()
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

func installHooks(embeddedHooks map[string]string, force bool, shared bool) error {
	// Get actual git directory (handles worktrees where .git is a file)
	gitDir, err := getGitDir()
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
			// Hook exists - back it up unless force is set
			if !force {
				backupPath := hookPath + ".backup"
				if err := os.Rename(hookPath, backupPath); err != nil {
					return fmt.Errorf("failed to backup %s: %w", hookName, err)
				}
			}
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
	gitDir, err := getGitDir()
	if err != nil {
		return err
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}

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

func init() {
	hooksInstallCmd.Flags().Bool("force", false, "Overwrite existing hooks without backup")
	hooksInstallCmd.Flags().Bool("shared", false, "Install hooks to .beads-hooks/ (versioned) instead of .git/hooks/")

	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	hooksCmd.AddCommand(hooksListCmd)

	rootCmd.AddCommand(hooksCmd)
}
