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

var promoteCmd = &cobra.Command{
	Use:     "promote <wisp-id>",
	GroupID: "issues",
	Short:   "Promote a wisp to a permanent bead",
	Long: `Promote a wisp (ephemeral issue) to a permanent bead (Level 1).

This sets ephemeral=false on the issue, making it persistent and exportable
to JSONL. The original ID is preserved so all links keep working.

A comment is added recording the promotion and optional reason.

Examples:
  bd promote bd-abc123
  bd promote bd-abc123 --reason "Worth tracking long-term"`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("promote")

		id := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		ctx := rootCtx

		// Build promotion comment
		comment := "Promoted from Level 0"
		if reason != "" {
			comment += ": " + reason
		}

		// Resolve partial ID and handle routing
		if daemonClient != nil {
			// Check if this ID needs routing to a different beads directory
			if needsRouting(id) {
				promoteRouted(id, comment)
				return
			}

			resolveArgs := &rpc.ResolveIDArgs{ID: id}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				FatalErrorRespectJSON("resolving ID %s: %v", id, err)
			}
			var resolvedID string
			if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
				FatalErrorRespectJSON("unmarshaling resolved ID: %v", err)
			}
			id = resolvedID

			// Verify the issue is actually a wisp
			showArgs := &rpc.ShowArgs{ID: id}
			showResp, err := daemonClient.Show(showArgs)
			if err != nil {
				FatalErrorRespectJSON("getting issue %s: %v", id, err)
			}
			var issue types.Issue
			if err := json.Unmarshal(showResp.Data, &issue); err != nil {
				FatalErrorRespectJSON("decoding issue: %v", err)
			}
			if !issue.Ephemeral {
				FatalErrorRespectJSON("%s is not a wisp (already persistent)", id)
			}

			// Set ephemeral=false via update RPC
			ephemeral := false
			updateArgs := &rpc.UpdateArgs{
				ID:        id,
				Ephemeral: &ephemeral,
			}
			if _, err := daemonClient.Update(updateArgs); err != nil {
				FatalErrorRespectJSON("promoting %s: %v", id, err)
			}

			// Add promotion comment
			commentArgs := &rpc.CommentAddArgs{
				ID:     id,
				Author: actor,
				Text:   comment,
			}
			if _, err := daemonClient.AddComment(commentArgs); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", id, err)
			}

			if jsonOutput {
				// Re-fetch to return updated issue
				showResp2, err := daemonClient.Show(showArgs)
				if err == nil {
					var updated types.Issue
					if err := json.Unmarshal(showResp2.Data, &updated); err == nil {
						outputJSON(&updated)
					}
				}
			} else {
				fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), id)
			}
			return
		}

		// Direct mode
		if store == nil {
			fmt.Fprintln(os.Stderr, "Error: database not initialized")
			os.Exit(1)
		}

		fullID, err := utils.ResolvePartialID(ctx, store, id)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", id, err)
		}

		// Verify the issue is actually a wisp
		issue, err := store.GetIssue(ctx, fullID)
		if err != nil {
			FatalErrorRespectJSON("getting issue %s: %v", fullID, err)
		}
		if issue == nil {
			FatalErrorRespectJSON("issue %s not found", fullID)
		}
		if !issue.Ephemeral {
			FatalErrorRespectJSON("%s is not a wisp (already persistent)", fullID)
		}

		// Set ephemeral=false
		updates := map[string]interface{}{
			"wisp": false,
		}
		if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
			FatalErrorRespectJSON("promoting %s: %v", fullID, err)
		}

		// Add promotion comment
		if err := store.AddComment(ctx, fullID, actor, comment); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", fullID, err)
		}

		if jsonOutput {
			updated, _ := store.GetIssue(ctx, fullID)
			if updated != nil {
				outputJSON(updated)
			}
		} else {
			fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), fullID)
		}
	},
}

// promoteRouted handles promotion for cross-rig routed issues.
func promoteRouted(id, comment string) {
	result, err := resolveAndGetIssueWithRouting(rootCtx, store, id)
	if err != nil {
		FatalErrorRespectJSON("resolving %s: %v", id, err)
	}
	if result == nil || result.Issue == nil {
		if result != nil {
			result.Close()
		}
		FatalErrorRespectJSON("issue %s not found", id)
	}
	defer result.Close()

	if !result.Issue.Ephemeral {
		FatalErrorRespectJSON("%s is not a wisp (already persistent)", id)
	}

	updates := map[string]interface{}{
		"wisp": false,
	}
	if err := result.Store.UpdateIssue(rootCtx, result.ResolvedID, updates, actor); err != nil {
		FatalErrorRespectJSON("promoting %s: %v", id, err)
	}

	if err := result.Store.AddComment(rootCtx, result.ResolvedID, actor, comment); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", id, err)
	}

	if jsonOutput {
		updated, _ := result.Store.GetIssue(rootCtx, result.ResolvedID)
		if updated != nil {
			outputJSON(updated)
		}
	} else {
		fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), result.ResolvedID)
	}
}

func init() {
	promoteCmd.Flags().StringP("reason", "r", "", "Reason for promotion")
	promoteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(promoteCmd)
}
