package main

import (
	"context"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var reopenCmd = &cobra.Command{
	Use:   "reopen [id...]",
	Short: "Reopen one or more closed issues",
	Long: `Reopen closed issues by setting status to 'open' and clearing the closed_at timestamp.

This is more explicit than 'bd update --status open' and emits a Reopened event.`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		reason, _ := cmd.Flags().GetString("reason")

		ctx := context.Background()
		reopenedIssues := []*types.Issue{}
		for _, id := range args {
			// UpdateIssue automatically clears closed_at when status changes from closed
			updates := map[string]interface{}{
				"status": string(types.StatusOpen),
			}
			if err := store.UpdateIssue(ctx, id, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", id, err)
				continue
			}

			// Add reason as a comment if provided
			if reason != "" {
				if err := store.AddComment(ctx, id, actor, reason); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add comment to %s: %v\n", id, err)
				}
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, id)
				if issue != nil {
					reopenedIssues = append(reopenedIssues, issue)
				}
			} else {
				blue := color.New(color.FgBlue).SprintFunc()
				reasonMsg := ""
				if reason != "" {
					reasonMsg = ": " + reason
				}
				fmt.Printf("%s Reopened %s%s\n", blue("↻"), id, reasonMsg)
			}
		}

		// Schedule auto-flush if any issues were reopened
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(reopenedIssues) > 0 {
			outputJSON(reopenedIssues)
		}
	},
}

func init() {
	reopenCmd.Flags().StringP("reason", "r", "", "Reason for reopening")
	rootCmd.AddCommand(reopenCmd)
}
