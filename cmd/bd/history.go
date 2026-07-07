package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var (
	historyLimit int
)

var historyCmd = &cobra.Command{
	Use:     "history <id>",
	GroupID: "views",
	Short:   "Show version history for an issue",
	Long: `Show the complete version history of an issue, including all commits
where the issue was modified.

Examples:
  bd history bd-123           # Show all history for issue bd-123
  bd history bd-123 --limit 5 # Show last 5 changes`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("history")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx
		issueID := args[0]

		history, err := store.History(ctx, issueID)
		if err != nil {
			return HandleErrorRespectJSON("failed to get history: %v", err)
		}

		if len(history) == 0 {
			if jsonOutput {
				return outputJSON(history)
			}
			fmt.Printf("No history found for issue %s\n", issueID)
			return nil
		}

		if historyLimit > 0 && historyLimit < len(history) {
			history = history[:historyLimit]
		}

		if jsonOutput {
			return outputJSON(history)
		}

		fmt.Printf("\n%s History for %s (%d entries)\n\n",
			ui.RenderAccent("📜"), issueID, len(history))

		for i, entry := range history {
			fmt.Printf("%s %s\n",
				ui.RenderMuted(entry.CommitHash[:8]),
				ui.RenderMuted(entry.CommitDate.Format("2006-01-02 15:04:05")))
			fmt.Printf("  Author: %s\n", entry.Committer)

			if entry.Issue != nil {
				statusIcon := ui.GetStatusIcon(string(entry.Issue.Status))
				fmt.Printf("  %s %s: %s [P%d - %s]\n",
					statusIcon,
					entry.Issue.ID,
					entry.Issue.Title,
					entry.Issue.Priority,
					entry.Issue.Status)
			}

			if i < len(history)-1 {
				fmt.Println()
			}
		}
		fmt.Println()
		return nil
	},
}

func init() {
	historyCmd.Flags().IntVar(&historyLimit, "limit", 0, "Limit number of history entries (0 = all)")
	historyCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(historyCmd)
}
