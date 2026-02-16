package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
)

var (
	historyLimit int
)

var historyCmd = &cobra.Command{
	Use:     "history <id>",
	GroupID: "views",
	Short:   "Show version history for an issue (requires Dolt backend)",
	Long: `Show the complete version history of an issue, including all commits
where the issue was modified.

This command requires the Dolt storage backend. If you're using SQLite,
you'll see an error message suggesting to use Dolt for versioning features.

Examples:
  bd history bd-123           # Show all history for issue bd-123
  bd history bd-123 --limit 5 # Show last 5 changes`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		issueID := args[0]


		// Get issue history
		history, err := store.History(ctx, issueID)
		if err != nil {
			FatalErrorRespectJSON("failed to get history: %v", err)
		}

		if len(history) == 0 {
			fmt.Printf("No history found for issue %s\n", issueID)
			return
		}

		// Apply limit if specified
		if historyLimit > 0 && historyLimit < len(history) {
			history = history[:historyLimit]
		}

		if jsonOutput {
			outputJSON(history)
			return
		}

		// Display history in human-readable format
		fmt.Printf("\n%s History for %s (%d entries)\n\n",
			ui.RenderAccent("📜"), issueID, len(history))

		for i, entry := range history {
			// Commit info line
			fmt.Printf("%s %s\n",
				ui.RenderMuted(entry.CommitHash[:8]),
				ui.RenderMuted(entry.CommitDate.Format("2006-01-02 15:04:05")))
			fmt.Printf("  Author: %s\n", entry.Committer)

			if entry.Issue != nil {
				// Show issue state at this commit
				statusIcon := ui.GetStatusIcon(string(entry.Issue.Status))
				fmt.Printf("  %s %s: %s [P%d - %s]\n",
					statusIcon,
					entry.Issue.ID,
					entry.Issue.Title,
					entry.Issue.Priority,
					entry.Issue.Status)
			}

			// Separator between entries
			if i < len(history)-1 {
				fmt.Println()
			}
		}
		fmt.Println()
	},
}

func init() {
	historyCmd.Flags().IntVar(&historyLimit, "limit", 0, "Limit number of history entries (0 = all)")
	historyCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(historyCmd)
}
