package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var branchDelete bool

var branchCmd = &cobra.Command{
	Use:     "branch [name]",
	GroupID: "sync",
	Short:   "List, create, or delete branches",
	Long: `List all branches, create a new branch, or delete an existing branch.

This command requires the Dolt storage backend. Without arguments,
it lists all branches. With an argument, it creates a new branch.
With -d, it deletes the named branch.

Examples:
  bd branch                    # List all branches
  bd branch feature-xyz        # Create a new branch named feature-xyz
  bd branch -d feature-xyz     # Delete branch feature-xyz`,
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

		if branchDelete {
			if len(args) == 0 {
				return HandleErrorRespectJSON("branch name required for deletion")
			}
			branchName := args[0]

			currentBranch, err := store.CurrentBranch(ctx)
			if err == nil && currentBranch == branchName {
				return HandleErrorRespectJSON("cannot delete the currently checked-out branch %q", branchName)
			}

			if err := store.DeleteBranch(ctx, branchName); err != nil {
				return HandleErrorRespectJSON("failed to delete branch: %v", err)
			}

			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"deleted": branchName,
				})
			}

			fmt.Printf("Deleted branch: %s\n", ui.RenderAccent(branchName))
			return nil
		}

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
	branchCmd.Flags().BoolVarP(&branchDelete, "delete", "d", false, "Delete the named branch")
	rootCmd.AddCommand(branchCmd)
}
