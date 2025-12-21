package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/merge"
)

var (
	debugMerge bool
)

var mergeCmd = &cobra.Command{
	Use:     "merge <output> <base> <left> <right>",
	GroupID: "sync",
	Short:   "Git merge driver for beads JSONL files",
	Long: `bd merge is a git merge driver for beads issue tracker JSONL files.

NOTE: This command is for git merge operations, NOT for merging duplicate issues.
To merge duplicate issues, use 'bd duplicates --auto-merge' instead.

This tool handles 3-way merges during git pull/merge operations. It intelligently
merges issues based on identity (id + created_at + created_by), applies field-specific
merge rules, combines dependencies, and outputs conflict markers for unresolvable conflicts.

Designed to work as a git merge driver. Configure with:

  git config merge.beads.driver "bd merge %A %O %A %B"
  git config merge.beads.name "bd JSONL merge driver"
  echo ".beads/issues.jsonl merge=beads" >> .gitattributes

Or use 'bd init' which automatically configures the merge driver.

Exit codes:
  0 - Merge successful (no conflicts)
  1 - Merge completed with conflicts (conflict markers in output)
  2 - Error (invalid arguments, file not found, etc.)

Original tool by @neongreen: https://github.com/neongreen/mono/tree/main/beads-merge
Vendored into bd with permission.`,
	Args: cobra.ExactArgs(4),
	// PreRun disables PersistentPreRun for this command (no database needed)
	PreRun: func(cmd *cobra.Command, args []string) {},
	Run: func(cmd *cobra.Command, args []string) {
		outputPath := args[0]
		basePath := args[1]
		leftPath := args[2]
		rightPath := args[3]

		// Log arguments for debugging
		if debugMerge {
			fmt.Fprintf(os.Stderr, "=== MERGE DRIVER INVOKED ===\n")
			fmt.Fprintf(os.Stderr, "Arguments received:\n")
			fmt.Fprintf(os.Stderr, "  %%A (output): %q\n", outputPath)
			fmt.Fprintf(os.Stderr, "  %%O (base):   %q\n", basePath)
			fmt.Fprintf(os.Stderr, "  %%L (left):   %q\n", leftPath)
			fmt.Fprintf(os.Stderr, "  %%R (right):  %q\n", rightPath)
			fmt.Fprintf(os.Stderr, "\nFile existence check:\n")
			for i, path := range []string{outputPath, basePath, leftPath, rightPath} {
				label := []string{"%%A (output)", "%%O (base)", "%%L (left)", "%%R (right)"}[i]
				if _, err := os.Stat(path); err == nil {
					fmt.Fprintf(os.Stderr, "  %s: EXISTS\n", label)
				} else {
					fmt.Fprintf(os.Stderr, "  %s: NOT FOUND - %v\n", label, err)
				}
			}
			fmt.Fprintf(os.Stderr, "\n")
		}

		// Ensure cleanup runs after merge completes
		defer func() {
			cleanupMergeArtifacts(outputPath, debugMerge)
		}()

		err := merge.Merge3Way(outputPath, basePath, leftPath, rightPath, debugMerge)
		if err != nil {
			// Check if error is due to conflicts
			if err.Error() == fmt.Sprintf("merge completed with %d conflicts", 1) ||
			   err.Error() == fmt.Sprintf("merge completed with %d conflicts", 2) ||
			   err.Error()[:len("merge completed with")] == "merge completed with" {
				// Conflicts present - exit with 1 (standard for merge drivers)
				os.Exit(1)
			}
			// Other errors - exit with 2
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(2)
		}
		// Success - exit with 0
		os.Exit(0)
	},
}

func cleanupMergeArtifacts(outputPath string, debug bool) {
	// Determine the .beads directory from the output path
	// outputPath is typically .beads/issues.jsonl
	beadsDir := filepath.Dir(outputPath)

	if debug {
		fmt.Fprintf(os.Stderr, "=== CLEANUP ===\n")
		fmt.Fprintf(os.Stderr, "Cleaning up artifacts in: %s\n", beadsDir)
	}

	// 1. Find and remove any files with "backup" in the name
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "Warning: failed to read directory for cleanup: %v\n", err)
		}
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.Contains(strings.ToLower(entry.Name()), "backup") {
			fullPath := filepath.Join(beadsDir, entry.Name())

			// Try to git rm if tracked
			// #nosec G204 -- fullPath is safely constructed via filepath.Join from entry.Name()
			// from os.ReadDir. exec.Command does NOT use shell interpretation - arguments
			// are passed directly to git binary. See TestCleanupMergeArtifacts_CommandInjectionPrevention
			gitRmCmd := exec.Command("git", "rm", "-f", "--quiet", fullPath)
			gitRmCmd.Dir = filepath.Dir(beadsDir)
			_ = gitRmCmd.Run() // Ignore errors, file may not be tracked

			// Also remove from filesystem if git rm didn't work
			if err := os.Remove(fullPath); err == nil {
				if debug {
					fmt.Fprintf(os.Stderr, "Removed backup file: %s\n", entry.Name())
				}
			}
		}
	}

	// 2. Run git clean -f in .beads/ directory to remove untracked files
	cleanCmd := exec.Command("git", "clean", "-f")
	cleanCmd.Dir = beadsDir
	if debug {
		cleanCmd.Stderr = os.Stderr
		cleanCmd.Stdout = os.Stderr
		fmt.Fprintf(os.Stderr, "Running: git clean -f in %s\n", beadsDir)
	}
	_ = cleanCmd.Run() // Ignore errors, git clean may fail in some contexts

	if debug {
		fmt.Fprintf(os.Stderr, "Cleanup complete\n\n")
	}
}

func init() {
	mergeCmd.Flags().BoolVar(&debugMerge, "debug", false, "Enable debug output to stderr")
	rootCmd.AddCommand(mergeCmd)
}
