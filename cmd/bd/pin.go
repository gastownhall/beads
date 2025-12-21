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

var pinCmd = &cobra.Command{
	Use:     "pin [id...]",
	GroupID: "issues",
	Short:   "Pin one or more issues as persistent context markers",
	Long: `Pin issues to mark them as persistent context markers.

Pinned issues are not work items - they are context beads that should
remain visible and not be cleaned up or closed automatically.

Examples:
  bd pin bd-abc        # Pin a single issue
  bd pin bd-abc bd-def # Pin multiple issues`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("pin")

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

		pinnedIssues := []*types.Issue{}

		// If daemon is running, use RPC
		if daemonClient != nil {
			for _, id := range resolvedIDs {
				pinned := true
				updateArgs := &rpc.UpdateArgs{
					ID:     id,
					Pinned: &pinned,
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error pinning %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						pinnedIssues = append(pinnedIssues, &issue)
					}
				} else {
					
					fmt.Printf("%s Pinned %s\n", ui.RenderPass("📌"), id)
				}
			}

			if jsonOutput && len(pinnedIssues) > 0 {
				outputJSON(pinnedIssues)
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
				"pinned": true,
			}

			if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error pinning %s: %v\n", fullID, err)
				continue
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, fullID)
				if issue != nil {
					pinnedIssues = append(pinnedIssues, issue)
				}
			} else {
				
				fmt.Printf("%s Pinned %s\n", ui.RenderPass("📌"), fullID)
			}
		}

		// Schedule auto-flush if any issues were pinned
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(pinnedIssues) > 0 {
			outputJSON(pinnedIssues)
		}
	},
}

func init() {
	rootCmd.AddCommand(pinCmd)
}
