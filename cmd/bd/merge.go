package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/merge"
)

var (
	debugMerge bool
)

var mergeCmd = &cobra.Command{
	Use:   "merge <output> <base> <left> <right>",
	Short: "3-way merge tool for beads JSONL issue files",
	Long: `bd merge is a 3-way merge tool for beads issue tracker JSONL files.

It intelligently merges issues based on identity (id + created_at + created_by),
applies field-specific merge rules, combines dependencies, and outputs conflict
markers for unresolvable conflicts.

Designed to work as a git merge driver. Configure with:

  git config merge.beads.driver "bd merge %A %O %L %R"
  git config merge.beads.name "bd JSONL merge driver"
  echo ".beads/beads.jsonl merge=beads" >> .gitattributes

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

func init() {
	mergeCmd.Flags().BoolVar(&debugMerge, "debug", false, "Enable debug output to stderr")
	rootCmd.AddCommand(mergeCmd)
}
