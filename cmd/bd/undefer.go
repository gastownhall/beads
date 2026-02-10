package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
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
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("undefer")

		ctx := rootCtx

		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			for _, id := range args {
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error resolving ID %s: %v\n", id, err)
					os.Exit(1)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
					os.Exit(1)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		undeferredIssues := []*types.Issue{}

		// If daemon is running, use RPC
		if daemonClient != nil {
			for _, id := range resolvedIDs {
				status := string(types.StatusOpen)
				emptyStr := "" // Clear defer_until by sending empty string (GH#820)
				updateArgs := &rpc.UpdateArgs{
					ID:         id,
					Status:     &status,
					DeferUntil: &emptyStr, // Clear defer_until timestamp
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error undeferring %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						undeferredIssues = append(undeferredIssues, &issue)
					}
				} else {
					fmt.Printf("%s Undeferred %s (now open)\n", ui.RenderPass("*"), id)
				}
			}

			if jsonOutput && len(undeferredIssues) > 0 {
				outputJSON(undeferredIssues)
			}
			return
		}

		// Fall back to direct storage access
		if store == nil {
			fmt.Fprintln(os.Stderr, "Error: database not initialized")
			os.Exit(1)
		}

		for _, id := range args {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}

			updates := map[string]interface{}{
				"status":      string(types.StatusOpen),
				"defer_until": nil, // Clear defer_until timestamp (GH#820)
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

		if jsonOutput && len(undeferredIssues) > 0 {
			outputJSON(undeferredIssues)
		}
	},
}

func init() {
	undeferCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(undeferCmd)
}
