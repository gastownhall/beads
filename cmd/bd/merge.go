package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/merge"
)

var (
	mergeDebug bool
	mergeInto  string
	mergeDryRun bool
)

var mergeCmd = &cobra.Command{
	Use:   "merge <source-ids...> --into <target-id> | merge <output> <base> <left> <right>",
	Short: "Merge duplicate issues or perform 3-way JSONL merge",
	Long: `Two modes of operation:

1. Duplicate issue merge (--into flag):
   bd merge <source-id...> --into <target-id>
   Consolidates duplicate issues into a single target issue.

2. Git 3-way merge (4 positional args, no --into):
   bd merge <output> <base> <left> <right>
   Performs intelligent field-level JSONL merging for git merge driver.

Git merge mode implements:
- Dependencies merged with union + dedup
- Timestamps use max(left, right)
- Status/priority use 3-way comparison
- Detects deleted-vs-modified conflicts

Git merge driver setup:
  git config merge.beads.driver "bd merge %A %O %L %R"

Exit codes:
  0 - Clean merge (no conflicts)
  1 - Conflicts found (conflict markers written to output)
  Other - Error occurred`,
	Args: cobra.MinimumNArgs(1),
	// Skip database initialization check for git merge mode
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// If this is git merge mode (4 args, no --into), skip normal DB init
		if mergeInto == "" && len(args) == 4 {
			return
		}
		// Otherwise, run the normal PersistentPreRun
		if rootCmd.PersistentPreRun != nil {
			rootCmd.PersistentPreRun(cmd, args)
		}
	},
	RunE: runMerge,
}

func init() {
	mergeCmd.Flags().BoolVar(&mergeDebug, "debug", false, "Enable debug output")
	mergeCmd.Flags().StringVar(&mergeInto, "into", "", "Target issue ID for duplicate merge")
	mergeCmd.Flags().BoolVar(&mergeDryRun, "dry-run", false, "Preview merge without applying changes")
	rootCmd.AddCommand(mergeCmd)
}

func runMerge(cmd *cobra.Command, args []string) error {
	// Determine mode based on arguments
	if mergeInto != "" {
		// Duplicate issue merge mode
		return runDuplicateMerge(cmd, args)
	} else if len(args) == 4 {
		// Git 3-way merge mode
		return runGitMerge(cmd, args)
	} else {
		return fmt.Errorf("invalid arguments: use either '<source-ids...> --into <target-id>' or '<output> <base> <left> <right>'")
	}
}

func runGitMerge(_ *cobra.Command, args []string) error {
	outputPath := args[0]
	basePath := args[1]
	leftPath := args[2]
	rightPath := args[3]

	if mergeDebug {
		fmt.Fprintf(os.Stderr, "Merging:\n")
		fmt.Fprintf(os.Stderr, "  Base:   %s\n", basePath)
		fmt.Fprintf(os.Stderr, "  Left:   %s\n", leftPath)
		fmt.Fprintf(os.Stderr, "  Right:  %s\n", rightPath)
		fmt.Fprintf(os.Stderr, "  Output: %s\n", outputPath)
	}

	// Perform the merge
	hasConflicts, err := merge.MergeFiles(outputPath, basePath, leftPath, rightPath, mergeDebug)
	if err != nil {
		return fmt.Errorf("merge failed: %w", err)
	}

	if hasConflicts {
		if mergeDebug {
			fmt.Fprintf(os.Stderr, "Merge completed with conflicts\n")
		}
		os.Exit(1)
	}

	if mergeDebug {
		fmt.Fprintf(os.Stderr, "Merge completed successfully\n")
	}
	return nil
}

func runDuplicateMerge(cmd *cobra.Command, sourceIDs []string) error {
	// This will be implemented later or moved from duplicates.go
	return fmt.Errorf("duplicate issue merge not yet implemented - use 'bd duplicates --auto-merge' for now")
}
