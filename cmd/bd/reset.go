package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/ui"
)

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Remove all beads data and configuration",
	Long: `Reset beads to an uninitialized state, removing all local data.

This command removes:
  - The .beads directory (database, JSONL, config)
  - Git hooks installed by bd
  - Merge driver configuration
  - Sync branch worktrees

By default, shows what would be deleted (dry-run mode).
Use --force to actually perform the reset.

Examples:
  bd reset              # Show what would be deleted
  bd reset --force      # Actually delete everything`,
	Run: runReset,
}

func init() {
	resetCmd.Flags().Bool("force", false, "Actually perform the reset (required)")
	// Note: resetCmd is added to adminCmd in admin.go
}

func runReset(cmd *cobra.Command, args []string) {
	CheckReadonly("reset")

	force, _ := cmd.Flags().GetBool("force")

	// Get common git directory (for hooks and beads-worktrees, which are shared across worktrees)
	gitCommonDir, err := git.GetGitCommonDir()
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error": "not a git repository",
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: not a git repository\n")
		}
		os.Exit(1)
	}

	// Check if .beads directory exists
	beadsDir := ".beads"
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"message": "beads not initialized",
				"reset":   false,
			})
		} else {
			fmt.Println("Beads is not initialized in this repository.")
			fmt.Println("Nothing to reset.")
		}
		return
	}

	// Collect what would be deleted
	items := collectResetItems(gitCommonDir, beadsDir)

	if !force {
		// Dry-run mode: show what would be deleted
		showResetPreview(items)
		return
	}

	// Actually perform the reset
	performReset(items, gitCommonDir, beadsDir)
}

type resetItem struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

func collectResetItems(gitCommonDir, beadsDir string) []resetItem {
	var items []resetItem

	// Check for running daemon
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	if _, err := os.Stat(pidFile); err == nil {
		if isRunning, pid := isDaemonRunning(pidFile); isRunning {
			items = append(items, resetItem{
				Type:        "daemon",
				Path:        pidFile,
				Description: fmt.Sprintf("Stop running daemon (PID %d)", pid),
			})
		}
	}

	// Check for git hooks (hooks are in common git dir, shared across worktrees)
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}
	hooksDir := filepath.Join(gitCommonDir, "hooks")
	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		if _, err := os.Stat(hookPath); err == nil {
			// Check if it's a beads hook by looking for version marker
			if isBdHook(hookPath) {
				items = append(items, resetItem{
					Type:        "hook",
					Path:        hookPath,
					Description: fmt.Sprintf("Remove git hook: %s", hookName),
				})
			}
		}
	}

	// Check for merge driver config
	if hasMergeDriverConfig() {
		items = append(items, resetItem{
			Type:        "config",
			Path:        "merge.beads.*",
			Description: "Remove merge driver configuration",
		})
	}

	// Check for .gitattributes entry
	if hasGitattributesEntry() {
		items = append(items, resetItem{
			Type:        "gitattributes",
			Path:        ".gitattributes",
			Description: "Remove beads entry from .gitattributes",
		})
	}

	// Check for sync branch worktrees (in common git dir, shared across worktrees)
	worktreesDir := filepath.Join(gitCommonDir, "beads-worktrees")
	if info, err := os.Stat(worktreesDir); err == nil && info.IsDir() {
		items = append(items, resetItem{
			Type:        "worktrees",
			Path:        worktreesDir,
			Description: "Remove sync branch worktrees",
		})
	}

	// The .beads directory itself
	items = append(items, resetItem{
		Type:        "directory",
		Path:        beadsDir,
		Description: "Remove .beads directory (database, JSONL, config)",
	})

	return items
}

func isBdHook(hookPath string) bool {
	// #nosec G304 -- hook path is constructed from git dir, not user input
	file, err := os.Open(hookPath)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() && lineCount < 10 {
		line := scanner.Text()
		if strings.Contains(line, "bd-hooks-version:") || strings.Contains(line, "beads") {
			return true
		}
		lineCount++
	}
	return false
}

func hasMergeDriverConfig() bool {
	cmd := exec.Command("git", "config", "--get", "merge.beads.driver")
	if err := cmd.Run(); err == nil {
		return true
	}
	return false
}

func hasGitattributesEntry() bool {
	// #nosec G304 -- fixed path
	content, err := os.ReadFile(".gitattributes")
	if err != nil {
		return false
	}
	return strings.Contains(string(content), "merge=beads")
}

func showResetPreview(items []resetItem) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"dry_run": true,
			"items":   items,
		})
		return
	}

	fmt.Println(ui.RenderWarn("Reset preview (dry-run mode)"))
	fmt.Println()
	fmt.Println("The following will be removed:")
	fmt.Println()

	for _, item := range items {
		fmt.Printf("  %s %s\n", ui.RenderFail("•"), item.Description)
		if item.Type != "config" {
			fmt.Printf("    %s\n", item.Path)
		}
	}

	fmt.Println()
	fmt.Println(ui.RenderFail("⚠ This operation cannot be undone!"))
	fmt.Println()
	fmt.Printf("To proceed, run: %s\n", ui.RenderWarn("bd reset --force"))
}

func performReset(items []resetItem, _, beadsDir string) {

	var errors []string

	for _, item := range items {
		switch item.Type {
		case "daemon":
			pidFile := filepath.Join(beadsDir, "daemon.pid")
			stopDaemonQuiet(pidFile)
			if !jsonOutput {
				fmt.Printf("%s Stopped daemon\n", ui.RenderPass("✓"))
			}

		case "hook":
			if err := os.Remove(item.Path); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove hook %s: %v", item.Path, err))
			} else if !jsonOutput {
				fmt.Printf("%s Removed %s\n", ui.RenderPass("✓"), filepath.Base(item.Path))
			}
			// Restore backup if exists
			backupPath := item.Path + ".backup"
			if _, err := os.Stat(backupPath); err == nil {
				if err := os.Rename(backupPath, item.Path); err == nil && !jsonOutput {
					fmt.Printf("  Restored backup hook\n")
				}
			}

		case "config":
			// Remove merge driver config (ignore errors - may not exist)
			_ = exec.Command("git", "config", "--unset", "merge.beads.driver").Run()
			_ = exec.Command("git", "config", "--unset", "merge.beads.name").Run()
			if !jsonOutput {
				fmt.Printf("%s Removed merge driver config\n", ui.RenderPass("✓"))
			}

		case "gitattributes":
			if err := removeGitattributesEntry(); err != nil {
				errors = append(errors, fmt.Sprintf("failed to update .gitattributes: %v", err))
			} else if !jsonOutput {
				fmt.Printf("%s Updated .gitattributes\n", ui.RenderPass("✓"))
			}

		case "worktrees":
			if err := os.RemoveAll(item.Path); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove worktrees: %v", err))
			} else if !jsonOutput {
				fmt.Printf("%s Removed sync worktrees\n", ui.RenderPass("✓"))
			}

		case "directory":
			if err := os.RemoveAll(item.Path); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove .beads: %v", err))
			} else if !jsonOutput {
				fmt.Printf("%s Removed .beads directory\n", ui.RenderPass("✓"))
			}
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"reset":   true,
			"success": len(errors) == 0,
		}
		if len(errors) > 0 {
			result["errors"] = errors
		}
		outputJSON(result)
		return
	}

	fmt.Println()
	if len(errors) > 0 {
		fmt.Println("Completed with errors:")
		for _, e := range errors {
			fmt.Printf("  • %s\n", e)
		}
	} else {
		fmt.Printf("%s Reset complete\n", ui.RenderPass("✓"))
		fmt.Println()
		fmt.Println("To reinitialize beads, run: bd init")
	}
}

// stopDaemonQuiet stops the daemon without printing status messages
func stopDaemonQuiet(pidFile string) {
	isRunning, pid := isDaemonRunning(pidFile)
	if !isRunning {
		return
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}

	_ = sendStopSignal(process) // Best-effort graceful stop

	// Wait for daemon to stop gracefully
	for i := 0; i < daemonShutdownAttempts; i++ {
		time.Sleep(daemonShutdownPollInterval)
		if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
			return
		}
	}

	// Force kill if still running
	_ = process.Kill() // Best-effort force kill, process may have already exited
}

func removeGitattributesEntry() error {
	// #nosec G304 -- fixed path
	content, err := os.ReadFile(".gitattributes")
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	skipNextEmpty := false

	for _, line := range lines {
		// Skip lines containing beads merge configuration
		if strings.Contains(line, "merge=beads") {
			skipNextEmpty = true
			continue
		}

		// Skip beads-related comment lines
		if strings.Contains(line, "Use bd merge for beads JSONL files") {
			skipNextEmpty = true
			continue
		}

		// Skip empty lines that follow removed beads entries
		if skipNextEmpty && strings.TrimSpace(line) == "" {
			continue
		}

		skipNextEmpty = false

		// Keep the line
		newLines = append(newLines, line)
	}

	newContent := strings.Join(newLines, "\n")
	// Remove trailing empty lines
	newContent = strings.TrimRight(newContent, "\n")

	// If file is now empty or only whitespace, remove it
	if strings.TrimSpace(newContent) == "" {
		return os.Remove(".gitattributes")
	}

	// Add single trailing newline
	newContent += "\n"
	//nolint:gosec // G306: .gitattributes must be world-readable (0644)
	return os.WriteFile(".gitattributes", []byte(newContent), 0644)
}
