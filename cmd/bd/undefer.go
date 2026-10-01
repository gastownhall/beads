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

If an issue carries a defer_until timestamp but its status isn't
"deferred" (e.g. after an explicit --status change), undefer clears
the stray timestamp without touching status.

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

			// Gate on defer_until, not status alone (ga-bq3w5): bd ready hides
			// any issue with a future defer_until regardless of status, so
			// `bd update <id> --status open --defer <date>` leaves a status=open
			// issue permanently invisible with no status-based signal anywhere.
			// Mirrors GH#3233's `bd update --defer=""` gate (update.go): only
			// flip status to open when it was actually "deferred" — other
			// statuses shouldn't be clobbered just to clear a stray timestamp.
			wasDeferred := issue.Status == types.StatusDeferred
			if !wasDeferred && issue.DeferUntil == nil {
				fmt.Fprintf(os.Stderr, "%s is not deferred (status: %s)\n", fullID, string(issue.Status))
				continue
			}

			updates := map[string]interface{}{
				"defer_until": nil,
			}
			if wasDeferred {
				updates["status"] = string(types.StatusOpen)
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
			} else if wasDeferred {
				fmt.Printf("%s Undeferred %s (now open)\n", ui.RenderPass("*"), fullID)
			} else {
				fmt.Printf("%s Cleared stale defer_until on %s (status unchanged: %s)\n", ui.RenderPass("*"), fullID, string(issue.Status))
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
