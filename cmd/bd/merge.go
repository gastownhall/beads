package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var mergeStrategy string

var mergeCmd = &cobra.Command{
	Use:     "merge <branch>",
	GroupID: "sync",
	Short:   "Merge a branch into the current branch",
	Long: `Merge the specified branch into the current branch.

If there are merge conflicts, they will be reported. You can resolve
conflicts automatically with --strategy ours|theirs.

Examples:
  bd merge feature-xyz                    # Merge feature-xyz into current branch
  bd merge feature-xyz --strategy ours    # Merge, preferring our changes on conflict
  bd merge feature-xyz --strategy theirs  # Merge, preferring their changes on conflict`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("merge")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx
		branchName := args[0]

		conflicts, err := store.Merge(ctx, branchName)
		if err != nil {
			return HandleErrorRespectJSON("failed to merge branch: %v", err)
		}

		if len(conflicts) > 0 {
			if mergeStrategy != "" {
				for _, conflict := range conflicts {
					table := conflict.Field
					if table == "" {
						table = "issues"
					}
					if err := store.ResolveConflicts(ctx, table, mergeStrategy); err != nil {
						return HandleErrorRespectJSON("failed to resolve conflicts: %v", err)
					}
				}
				if jsonOutput {
					return outputJSON(map[string]interface{}{
						"merged":        branchName,
						"conflicts":     len(conflicts),
						"resolved_with": mergeStrategy,
					})
				}
				fmt.Printf("Merged %s with %d conflicts resolved using '%s' strategy\n",
					ui.RenderAccent(branchName), len(conflicts), mergeStrategy)
				return nil
			}

			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"merged":    branchName,
					"conflicts": conflicts,
				})
			}

			fmt.Printf("\n%s Merge completed with conflicts:\n\n", ui.RenderAccent("!!"))
			for _, conflict := range conflicts {
				fmt.Printf("  - Issue %s: field %q (ours=%v, theirs=%v)\n",
					conflict.IssueID, conflict.Field, conflict.OursValue, conflict.TheirsValue)
			}
			fmt.Printf("\nResolve with: bd merge %s --strategy [ours|theirs]\n\n", branchName)
			return nil
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"merged":    branchName,
				"conflicts": 0,
			})
		}

		fmt.Printf("Successfully merged %s\n", ui.RenderAccent(branchName))
		return nil
	},
}

func init() {
	mergeCmd.Flags().StringVar(&mergeStrategy, "strategy", "", "Conflict resolution strategy: 'ours' or 'theirs'")
	rootCmd.AddCommand(mergeCmd)
}
