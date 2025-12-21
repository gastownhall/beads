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

var unpinCmd = &cobra.Command{
	Use:     "unpin [id...]",
	GroupID: "issues",
	Short:   "Unpin one or more issues",
	Long: `Unpin issues to remove their persistent context marker status.

This restores the issue to a normal work item that can be cleaned up
or closed normally.

Examples:
  bd unpin bd-abc        # Unpin a single issue
  bd unpin bd-abc bd-def # Unpin multiple issues`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("unpin")

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

		unpinnedIssues := []*types.Issue{}

		// If daemon is running, use RPC
		if daemonClient != nil {
			for _, id := range resolvedIDs {
				pinned := false
				updateArgs := &rpc.UpdateArgs{
					ID:     id,
					Pinned: &pinned,
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error unpinning %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						unpinnedIssues = append(unpinnedIssues, &issue)
					}
				} else {
					fmt.Printf("%s Unpinned %s\n", ui.RenderWarn("📍"), id)
				}
			}

			if jsonOutput && len(unpinnedIssues) > 0 {
				outputJSON(unpinnedIssues)
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
				"pinned": false,
			}

			if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error unpinning %s: %v\n", fullID, err)
				continue
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, fullID)
				if issue != nil {
					unpinnedIssues = append(unpinnedIssues, issue)
				}
			} else {
				fmt.Printf("%s Unpinned %s\n", ui.RenderWarn("📍"), fullID)
			}
		}

		// Schedule auto-flush if any issues were unpinned
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(unpinnedIssues) > 0 {
			outputJSON(unpinnedIssues)
		}
	},
}

func init() {
	rootCmd.AddCommand(unpinCmd)
}
