package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var unclaimCmd = &cobra.Command{
	Use:     "unclaim [id...]",
	GroupID: "issues",
	Short:   "Release a claimed issue",
	Long: `Release a claimed issue by clearing the assignee and resetting status to 'open'.

Use this when an agent crashes mid-work or you need to abandon a claimed task.
The issue becomes available for re-claiming by other agents.

Examples:
  bd unclaim bd-123
  bd unclaim bd-123 --reason "Agent crashed"
  bd unclaim bd-123 bd-456`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("unclaim")
		reason, _ := cmd.Flags().GetString("reason")
		ctx := rootCtx

		unclaimedIssues := []*types.Issue{}
		hasError := false
		if store == nil {
			FatalErrorWithHint("database not initialized",
				diagHint())
		}
		for _, id := range args {
			// Resolve with prefix routing
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				hasError = true
				continue
			}
			fullID := result.ResolvedID
			issueStore := result.Store

			if err := issueStore.UnclaimIssue(ctx, fullID, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error unclaiming %s: %v\n", fullID, err)
				hasError = true
				result.Close()
				continue
			}

			if reason != "" {
				if _, err := issueStore.AddIssueComment(ctx, fullID, actor, reason); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add reason comment: %v\n", err)
				}
			}

			if jsonOutput {
				updated, _ := issueStore.GetIssue(ctx, fullID)
				if updated != nil {
					unclaimedIssues = append(unclaimedIssues, updated)
				}
			} else {
				reasonMsg := ""
				if reason != "" {
					reasonMsg = ": " + reason
				}
				fmt.Printf("%s Unclaimed %s%s\n", ui.RenderPass("✓"), fullID, reasonMsg)
			}
			result.Close()
		}

		commandDidWrite.Store(true)

		if jsonOutput && len(unclaimedIssues) > 0 {
			outputJSON(unclaimedIssues)
		}

		if hasError {
			os.Exit(1)
		}
	},
}

func init() {
	unclaimCmd.Flags().StringP("reason", "r", "", "Reason for unclaiming")
	unclaimCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(unclaimCmd)
}
