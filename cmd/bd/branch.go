package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var branchCmd = &cobra.Command{
	Use:     "branch [name]",
	GroupID: "sync",
	Short:   "List or create branches",
	Long: `List all branches or create a new branch.

This command requires the Dolt storage backend. Without arguments,
it lists all branches. With an argument, it creates a new branch.

Examples:
  bd branch                    # List all branches
  bd branch feature-xyz        # Create a new branch named feature-xyz`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("branch")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx

		if len(args) == 0 {
			branches, err := store.ListBranches(ctx)
			if err != nil {
				return HandleErrorRespectJSON("failed to list branches: %v", err)
			}

			currentBranch, err := store.CurrentBranch(ctx)
			if err != nil {
				currentBranch = ""
			}

			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"current":  currentBranch,
					"branches": branches,
				})
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
			return nil
		}

		branchName := args[0]
		if err := store.Branch(ctx, branchName); err != nil {
			return HandleErrorRespectJSON("failed to create branch: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"created": branchName,
			})
		}

		fmt.Printf("Created branch: %s\n", ui.RenderAccent(branchName))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(branchCmd)
}
