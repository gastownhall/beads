package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
)

var branchCmd = &cobra.Command{
	Use:     "branch [name]",
	GroupID: "sync",
	Short:   "List or create branches (requires Dolt backend)",
	Long: `List all branches or create a new branch.

This command requires the Dolt storage backend. Without arguments,
it lists all branches. With an argument, it creates a new branch.

Examples:
  bd branch                    # List all branches
  bd branch feature-xyz        # Create a new branch named feature-xyz`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// Check if storage supports versioning
		vs, ok := storage.AsVersioned(store)
		if !ok {
			FatalErrorRespectJSON("branch requires Dolt backend (current backend does not support versioning)")
		}

		// If no args, list branches
		if len(args) == 0 {
			branches, err := vs.ListBranches(ctx)
			if err != nil {
				FatalErrorRespectJSON("failed to list branches: %v", err)
			}

			currentBranch, err := vs.CurrentBranch(ctx)
			if err != nil {
				// Non-fatal, just don't show current marker
				currentBranch = ""
			}

			if jsonOutput {
				outputJSON(map[string]interface{}{
					"current":  currentBranch,
					"branches": branches,
				})
				return
			}

			fmt.Printf("\n%s Branches:\n\n", ui.RenderAccent("🌿"))
			for _, branch := range branches {
				if branch == currentBranch {
					fmt.Printf("  * %s\n", ui.StatusInProgressStyle.Render(branch))
				} else {
					fmt.Printf("    %s\n", branch)
				}
			}
			fmt.Println()
			return
		}

		// Create new branch
		branchName := args[0]
		if err := vs.Branch(ctx, branchName); err != nil {
			FatalErrorRespectJSON("failed to create branch: %v", err)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"created": branchName,
			})
			return
		}

		fmt.Printf("Created branch: %s\n", ui.RenderAccent(branchName))
	},
}

func init() {
	rootCmd.AddCommand(branchCmd)
}
