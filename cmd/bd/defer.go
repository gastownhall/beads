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

var deferCmd = &cobra.Command{
	Use:   "defer [id...]",
	Short: "Defer one or more issues for later",
	Long: `Defer issues to put them on ice for later.

Deferred issues are deliberately set aside - not blocked by anything specific,
just postponed for future consideration. Unlike blocked issues, there's no
dependency keeping them from being worked. Unlike closed issues, they will
be revisited.

Deferred issues don't show in 'bd ready' but remain visible in 'bd list'.

Examples:
  bd defer bd-abc        # Defer a single issue
  bd defer bd-abc bd-def # Defer multiple issues`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("defer")

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

		deferredIssues := []*types.Issue{}

		// If daemon is running, use RPC
		if daemonClient != nil {
			for _, id := range resolvedIDs {
				status := string(types.StatusDeferred)
				updateArgs := &rpc.UpdateArgs{
					ID:     id,
					Status: &status,
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error deferring %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						deferredIssues = append(deferredIssues, &issue)
					}
				} else {
					fmt.Printf("%s Deferred %s\n", ui.RenderAccent("*"), id)
				}
			}

			if jsonOutput && len(deferredIssues) > 0 {
				outputJSON(deferredIssues)
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
				"status": string(types.StatusDeferred),
			}

			if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error deferring %s: %v\n", fullID, err)
				continue
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, fullID)
				if issue != nil {
					deferredIssues = append(deferredIssues, issue)
				}
			} else {
				fmt.Printf("%s Deferred %s\n", ui.RenderAccent("*"), fullID)
			}
		}

		// Schedule auto-flush if any issues were deferred
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(deferredIssues) > 0 {
			outputJSON(deferredIssues)
		}
	},
}

func init() {
	rootCmd.AddCommand(deferCmd)
}
