package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/git"
)

var resetCmd = &cobra.Command{
	Use:   "reset [--confirm <remote-url>]",
	Short: "Completely remove beads from this repository",
	Long: `Completely remove beads from this repository, including all issue data.

This command:
1. Stops any running daemon
2. Removes git hooks installed by beads
3. Removes the merge driver configuration
4. Removes beads entry from .gitattributes
5. Deletes the .beads directory (ALL ISSUE DATA)
6. Removes the sync worktree (if exists)

WARNING: This permanently deletes all issue data. Consider backing up first:
  cp .beads/issues.jsonl ~/beads-backup-$(date +%Y%m%d).jsonl

SAFETY: You must pass --confirm with the git remote URL to confirm.

EXAMPLES:
  # Preview what would be removed
  bd reset --dry-run

  # Actually reset (requires confirmation)
  bd reset --confirm origin

  # Or with the full remote URL
  bd reset --confirm git@github.com:user/repo.git

After reset, you can reinitialize with:
  bd init`,
	Run: runReset,
}

var (
	resetConfirm string
	resetDryRun  bool
	resetForce   bool
)

func init() {
	resetCmd.Flags().StringVar(&resetConfirm, "confirm", "", "Remote name or URL to confirm reset (required)")
	resetCmd.Flags().BoolVar(&resetDryRun, "dry-run", false, "Preview what would be removed without making changes")
	resetCmd.Flags().BoolVar(&resetForce, "force", false, "Skip confirmation prompts")
	rootCmd.AddCommand(resetCmd)
}

func runReset(cmd *cobra.Command, args []string) {
	// Check if we're in a beads repository
	beadsDir := findBeadsDir()
	if beadsDir == "" {
		fmt.Fprintln(os.Stderr, "Error: No .beads directory found - nothing to reset")
		os.Exit(1)
	}

	// Get git root
	gitRoot, err := git.GetMainRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Not in a git repository: %v\n", err)
		os.Exit(1)
	}

	// Verify confirmation unless dry-run or force
	if !resetDryRun && !resetForce {
		if resetConfirm == "" {
			fmt.Fprintln(os.Stderr, color.RedString("Error: --confirm flag required"))
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "This command permanently deletes all issue data.")
			fmt.Fprintln(os.Stderr, "To confirm, pass the remote name or URL:")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "  bd reset --confirm origin")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Or use --dry-run to preview what would be removed:")
			fmt.Fprintln(os.Stderr, "  bd reset --dry-run")
			os.Exit(1)
		}

		// Verify the confirmation matches a remote
		if !verifyResetConfirmation(resetConfirm) {
			fmt.Fprintf(os.Stderr, color.RedString("Error: '%s' does not match any git remote\n"), resetConfirm)
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Available remotes:")
			listRemotes()
			os.Exit(1)
		}
	}

	if resetDryRun {
		fmt.Println(color.YellowString("DRY RUN - no changes will be made"))
		fmt.Println()
	}

	// Track what we'll do/did
	var actions []string

	// 1. Stop daemon
	fmt.Println("Checking for running daemon...")
	if resetDryRun {
		actions = append(actions, "Would stop daemon (if running)")
	} else {
		if err := stopDaemonForReset(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		} else {
			actions = append(actions, "Stopped daemon")
		}
	}

	// 2. Uninstall hooks
	fmt.Println("Checking git hooks...")
	if resetDryRun {
		hooks := CheckGitHooks()
		for _, h := range hooks {
			if h.Installed {
				actions = append(actions, fmt.Sprintf("Would remove hook: %s", h.Name))
			}
		}
	} else {
		if err := uninstallHooks(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to uninstall hooks: %v\n", err)
		} else {
			actions = append(actions, "Removed git hooks")
		}
	}

	// 3. Remove merge driver config
	fmt.Println("Checking merge driver config...")
	if resetDryRun {
		actions = append(actions, "Would remove merge driver config (git config)")
	} else {
		if err := removeMergeDriverConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		} else {
			actions = append(actions, "Removed merge driver config")
		}
	}

	// 4. Remove .gitattributes entry
	fmt.Println("Checking .gitattributes...")
	gitattributes := filepath.Join(gitRoot, ".gitattributes")
	if resetDryRun {
		if _, err := os.Stat(gitattributes); err == nil {
			actions = append(actions, "Would remove beads entry from .gitattributes")
		}
	} else {
		if err := removeBeadsFromGitattributes(gitattributes); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		} else {
			actions = append(actions, "Removed beads entry from .gitattributes")
		}
	}

	// 5. Remove .beads directory
	fmt.Println("Checking .beads directory...")
	if resetDryRun {
		// Count files
		fileCount := 0
		_ = filepath.Walk(beadsDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				fileCount++
			}
			return nil
		})
		actions = append(actions, fmt.Sprintf("Would delete .beads directory (%d files)", fileCount))
	} else {
		if err := os.RemoveAll(beadsDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to remove .beads directory: %v\n", err)
			os.Exit(1)
		}
		actions = append(actions, "Deleted .beads directory")
	}

	// 6. Remove sync worktree
	gitDir, _ := git.GetGitDir()
	worktreePath := filepath.Join(gitDir, "beads-worktrees")
	if _, err := os.Stat(worktreePath); err == nil {
		fmt.Println("Checking sync worktree...")
		if resetDryRun {
			actions = append(actions, "Would remove sync worktree")
		} else {
			// First try to remove the git worktree properly
			_ = exec.Command("git", "worktree", "remove", "--force", filepath.Join(worktreePath, "beads-sync")).Run()
			// Then remove the directory
			if err := os.RemoveAll(worktreePath); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to remove worktree: %v\n", err)
			} else {
				actions = append(actions, "Removed sync worktree")
			}
		}
	}

	// Summary
	fmt.Println()
	if resetDryRun {
		fmt.Println(color.YellowString("Actions that would be taken:"))
	} else {
		fmt.Println(color.GreenString("Reset complete!"))
	}
	for _, action := range actions {
		fmt.Printf("  %s %s\n", color.GreenString("✓"), action)
	}

	if !resetDryRun {
		fmt.Println()
		fmt.Println("To reinitialize beads, run:")
		fmt.Println("  bd init")
	}
}

// verifyResetConfirmation checks if the provided confirmation matches a remote
func verifyResetConfirmation(confirm string) bool {
	// Get list of remotes
	output, err := exec.Command("git", "remote", "-v").Output()
	if err != nil {
		return false
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			remoteName := parts[0]
			remoteURL := parts[1]

			// Match against remote name or URL
			if confirm == remoteName || confirm == remoteURL {
				return true
			}

			// Also match partial URLs (e.g., user/repo)
			if strings.Contains(remoteURL, confirm) {
				return true
			}
		}
	}

	return false
}

// listRemotes prints available git remotes
func listRemotes() {
	output, err := exec.Command("git", "remote", "-v").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "  (unable to list remotes)")
		return
	}

	// Dedupe (git remote -v shows each twice for fetch/push)
	seen := make(map[string]bool)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			key := parts[0] + " " + parts[1]
			if !seen[key] {
				seen[key] = true
				fmt.Printf("  %s\t%s\n", parts[0], parts[1])
			}
		}
	}
}

// stopDaemonForReset stops the daemon for this repository
func stopDaemonForReset() error {
	// Try to stop daemon via the daemon command
	cmd := exec.Command("bd", "daemon", "--stop")
	_ = cmd.Run() // Ignore errors - daemon might not be running

	// Also try killall
	cmd = exec.Command("bd", "daemons", "killall")
	_ = cmd.Run()

	return nil
}

// removeMergeDriverConfig removes the beads merge driver from git config
func removeMergeDriverConfig() error {
	// Remove merge driver settings
	_ = exec.Command("git", "config", "--unset", "merge.beads.driver").Run()
	_ = exec.Command("git", "config", "--unset", "merge.beads.name").Run()
	return nil
}

// removeBeadsFromGitattributes removes beads entries from .gitattributes
func removeBeadsFromGitattributes(path string) error {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil // Nothing to do
	}

	// Read the file
	// #nosec G304 -- path comes from gitRoot which is validated
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read .gitattributes: %w", err)
	}

	// Filter out beads-related lines
	var newLines []string
	inBeadsSection := false
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := scanner.Text()

		// Skip beads comment header
		if strings.Contains(line, "Use bd merge for beads") {
			inBeadsSection = true
			continue
		}

		// Skip beads merge attribute lines
		if strings.Contains(line, "merge=beads") {
			inBeadsSection = false
			continue
		}

		// Skip empty lines immediately after beads section
		if inBeadsSection && strings.TrimSpace(line) == "" {
			inBeadsSection = false
			continue
		}

		inBeadsSection = false
		newLines = append(newLines, line)
	}

	// If file would be empty (or just whitespace), remove it
	newContent := strings.Join(newLines, "\n")
	if strings.TrimSpace(newContent) == "" {
		return os.Remove(path)
	}

	// Write back
	// #nosec G306 -- .gitattributes should be readable
	return os.WriteFile(path, []byte(newContent+"\n"), 0644)
}
