package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/reset"
)

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset beads to a clean starting state",
	Long: `Reset beads to a clean starting state by clearing .beads/ and reinitializing.

This command is useful when:
- Your beads workspace is in an invalid state after an update
- You want to start fresh with issue tracking
- bd doctor cannot automatically fix problems

RESET MODES:

  Soft Reset (default):
    - Kills all daemons
    - Clears .beads/ directory
    - Reinitializes with bd init
    - Git history is unchanged

  Hard Reset (--hard):
    - Same as soft reset, plus:
    - Removes .beads/ files from git (git rm)
    - Creates a commit removing the old state
    - Creates a commit with fresh initialized state

OPTIONS:

  --backup      Create .beads-backup-{timestamp}/ before clearing
  --dry-run     Preview what would happen without making changes
  --force       Skip confirmation prompt
  --hard        Include git operations (git rm + commit)
  --skip-init   Don't reinitialize after clearing (leaves .beads/ empty)
  --verbose     Show detailed progress

EXAMPLES:

  bd reset              # Reset with confirmation prompt
  bd reset --backup     # Reset with backup first
  bd reset --dry-run    # Preview the impact
  bd reset --hard       # Reset including git history
  bd reset --force      # Reset without confirmation`,
	Run: func(cmd *cobra.Command, _ []string) {
		hard, _ := cmd.Flags().GetBool("hard")
		force, _ := cmd.Flags().GetBool("force")
		backup, _ := cmd.Flags().GetBool("backup")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		skipInit, _ := cmd.Flags().GetBool("skip-init")
		verbose, _ := cmd.Flags().GetBool("verbose")

		// Color helpers
		red := color.New(color.FgRed).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		green := color.New(color.FgGreen).SprintFunc()
		cyan := color.New(color.FgCyan).SprintFunc()

		// Validate state
		if err := reset.ValidateState(); err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", red("Error:"), err)
			os.Exit(1)
		}

		// Get impact summary
		impact, err := reset.CountImpact()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Failed to analyze workspace: %v\n", red("Error:"), err)
			os.Exit(1)
		}

		// Show impact summary
		fmt.Printf("\n%s Reset Impact Summary\n", yellow("⚠"))
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		totalIssues := impact.IssueCount - impact.TombstoneCount
		if totalIssues > 0 {
			fmt.Printf("  Issues to delete:     %s\n", cyan(fmt.Sprintf("%d", totalIssues)))
			fmt.Printf("    - Open:             %d\n", impact.OpenCount)
			fmt.Printf("    - Closed:           %d\n", impact.ClosedCount)
		} else {
			fmt.Printf("  Issues to delete:     %s\n", cyan("0"))
		}

		if impact.TombstoneCount > 0 {
			fmt.Printf("  Tombstones to delete: %s\n", cyan(fmt.Sprintf("%d", impact.TombstoneCount)))
		}

		if impact.HasUncommitted {
			fmt.Printf("  %s Uncommitted changes in .beads/ will be lost\n", yellow("⚠"))
		}

		fmt.Printf("\n")

		// Show what will happen
		fmt.Printf("Actions:\n")
		if backup {
			fmt.Printf("  1. Create backup (.beads-backup-{timestamp}/)\n")
		}
		fmt.Printf("  %s. Kill all daemons\n", actionNumber(backup, 1))
		if hard {
			fmt.Printf("  %s. Remove .beads/ from git index and commit\n", actionNumber(backup, 2))
		}
		fmt.Printf("  %s. Delete .beads/ directory\n", actionNumber(backup, hardOffset(hard, 2)))
		if !skipInit {
			fmt.Printf("  %s. Reinitialize workspace (bd init)\n", actionNumber(backup, hardOffset(hard, 3)))
			if hard {
				fmt.Printf("  %s. Commit fresh state to git\n", actionNumber(backup, hardOffset(hard, 4)))
			}
		}
		fmt.Printf("\n")

		// Dry run - stop here
		if dryRun {
			fmt.Printf("%s This was a dry run. No changes were made.\n\n", cyan("ℹ"))
			return
		}

		// Confirmation prompt (unless --force)
		if !force {
			fmt.Printf("%s This will permanently delete all issues. Continue? [y/N]: ", yellow("Warning:"))

			reader := bufio.NewReader(os.Stdin)
			response, err := reader.ReadString('\n')
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s Failed to read response: %v\n", red("Error:"), err)
				os.Exit(1)
			}

			response = strings.TrimSpace(strings.ToLower(response))
			if response != "y" && response != "yes" {
				fmt.Printf("Reset canceled.\n")
				return
			}
		}

		// Execute reset
		if verbose {
			fmt.Printf("\n%s Starting reset...\n", cyan("→"))
		}

		opts := reset.ResetOptions{
			Hard:     hard,
			Backup:   backup,
			DryRun:   false, // Already handled above
			SkipInit: skipInit,
		}

		result, err := reset.Reset(opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Reset failed: %v\n", red("Error:"), err)
			os.Exit(1)
		}

		// Handle --hard mode: commit fresh state after reinit
		if hard && !skipInit {
			beadsDir := beads.FindBeadsDir()
			if beadsDir != "" {
				commitMsg := "Initialize fresh beads workspace\n\nCreated new .beads/ directory after reset."
				if err := reset.GitAddAndCommit(beadsDir, commitMsg); err != nil {
					fmt.Fprintf(os.Stderr, "%s Failed to commit fresh state: %v\n", yellow("Warning:"), err)
					fmt.Fprintf(os.Stderr, "You may need to manually run: git add .beads && git commit -m \"Fresh beads state\"\n")
				} else if verbose {
					fmt.Printf("  %s Committed fresh state to git\n", green("✓"))
				}
			}
		}

		// Show results
		fmt.Printf("\n%s Reset complete!\n\n", green("✓"))

		if result.BackupPath != "" {
			fmt.Printf("  Backup created: %s\n", cyan(result.BackupPath))
		}

		if result.DaemonsKilled > 0 {
			fmt.Printf("  Daemons stopped: %d\n", result.DaemonsKilled)
		}

		if result.IssuesDeleted > 0 || result.TombstonesDeleted > 0 {
			fmt.Printf("  Issues deleted: %d\n", result.IssuesDeleted)
			if result.TombstonesDeleted > 0 {
				fmt.Printf("  Tombstones deleted: %d\n", result.TombstonesDeleted)
			}
		}

		if !skipInit {
			fmt.Printf("\n  Workspace reinitialized. Run %s to get started.\n", cyan("bd quickstart"))
		} else {
			fmt.Printf("\n  .beads/ directory has been cleared. Run %s to reinitialize.\n", cyan("bd init"))
		}
		fmt.Printf("\n")
	},
}

// actionNumber returns the step number accounting for backup offset
func actionNumber(hasBackup bool, step int) string {
	if hasBackup {
		return fmt.Sprintf("%d", step+1)
	}
	return fmt.Sprintf("%d", step)
}

// hardOffset adjusts step number for --hard mode which adds extra steps
func hardOffset(isHard bool, step int) int {
	if isHard {
		return step + 1
	}
	return step
}

func init() {
	resetCmd.Flags().Bool("hard", false, "Include git operations (git rm + commit)")
	resetCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")
	resetCmd.Flags().Bool("backup", false, "Create backup before reset")
	resetCmd.Flags().Bool("dry-run", false, "Preview what would happen without making changes")
	resetCmd.Flags().Bool("skip-init", false, "Don't reinitialize after clearing")
	resetCmd.Flags().BoolP("verbose", "v", false, "Show detailed progress")
	rootCmd.AddCommand(resetCmd)
}
