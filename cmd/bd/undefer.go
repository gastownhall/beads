package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var undeferCmd = &cobra.Command{
	Use:   "undefer [id...]",
	Short: "Undefer one or more issues (restore to open)",
	Long: `Undefer issues to restore them to open status.

This brings issues back from the icebox so they can be worked on again.
Issues will appear in 'bd ready' if they have no blockers.

Examples:
  bd undefer bd-abc        # Undefer a single issue
  bd undefer bd-abc bd-def # Undefer multiple issues`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("undefer")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		CheckReadonly("undefer")

		if usesProxiedServer() {
			return runUndeferProxiedServer(rootCtx, args)
		}

		ctx := rootCtx

		_, err := utils.ResolvePartialIDs(ctx, store, args)
		if err != nil {
			return HandleError("%v", err)
		}

		undeferredIssues := []*types.Issue{}

		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}

		for _, id := range args {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}

			issue, err := store.GetIssue(ctx, fullID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting %s: %v\n", fullID, err)
				continue
			}
			if issue.Status != types.StatusDeferred {
				fmt.Fprintf(os.Stderr, "%s is not deferred (status: %s)\n", fullID, string(issue.Status))
				continue
			}

			updates := map[string]interface{}{
				"status":      string(types.StatusOpen),
				"defer_until": nil,
			}

			if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error undeferring %s: %v\n", fullID, err)
				continue
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, fullID)
				if issue != nil {
					undeferredIssues = append(undeferredIssues, issue)
				}
			} else {
				fmt.Printf("%s Undeferred %s (now open)\n", ui.RenderPass("*"), fullID)
			}
		}

		if len(args) > 0 {
			commandDidWrite.Store(true)
		}

		if jsonOutput && len(undeferredIssues) > 0 {
			return outputJSON(undeferredIssues)
		}

		return nil
	},
}

func init() {
	undeferCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(undeferCmd)
}
