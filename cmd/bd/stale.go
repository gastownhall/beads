package main
import (
	"encoding/json"
	"fmt"
	"os"
	"time"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)
var staleCmd = &cobra.Command{
	Use:   "stale",
	Short: "Show stale issues (not updated recently)",
	Long: `Show issues that haven't been updated recently and may need attention.
This helps identify:
- In-progress issues with no recent activity (may be abandoned)
- Open issues that have been forgotten
- Issues that might be outdated or no longer relevant`,
	Run: func(cmd *cobra.Command, args []string) {
		days, _ := cmd.Flags().GetInt("days")
		status, _ := cmd.Flags().GetString("status")
		limit, _ := cmd.Flags().GetInt("limit")
		// Use global jsonOutput set by PersistentPreRun
		// Validate status if provided
		if status != "" && status != "open" && status != "in_progress" && status != "blocked" {
			fmt.Fprintf(os.Stderr, "Error: invalid status '%s'. Valid values: open, in_progress, blocked\n", status)
			os.Exit(1)
		}
		filter := types.StaleFilter{
			Days:   days,
			Status: status,
			Limit:  limit,
		}
		// If daemon is running, use RPC
		if daemonClient != nil {
			staleArgs := &rpc.StaleArgs{
				Days:   days,
				Status: status,
				Limit:  limit,
			}
			resp, err := daemonClient.Stale(staleArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			var issues []*types.Issue
			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}
			if jsonOutput {
				if issues == nil {
					issues = []*types.Issue{}
				}
				outputJSON(issues)
				return
			}
			displayStaleIssues(issues, days)
			return
		}
		// Direct mode
		ctx := rootCtx

		// Check database freshness before reading (bd-2q6d, bd-c4rq)
		// Skip check when using daemon (daemon auto-imports on staleness)
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		issues, err := store.GetStaleIssues(ctx, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if jsonOutput {
			if issues == nil {
				issues = []*types.Issue{}
			}
			outputJSON(issues)
			return
		}
		displayStaleIssues(issues, days)
	},
}
func displayStaleIssues(issues []*types.Issue, days int) {
	if len(issues) == 0 {
		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("\n%s No stale issues found (all active)\n\n", green("✨"))
		return
	}
	yellow := color.New(color.FgYellow).SprintFunc()
	fmt.Printf("\n%s Stale issues (%d not updated in %d+ days):\n\n", yellow("⏰"), len(issues), days)
	now := time.Now()
	for i, issue := range issues {
		daysStale := int(now.Sub(issue.UpdatedAt).Hours() / 24)
		fmt.Printf("%d. [P%d] %s: %s\n", i+1, issue.Priority, issue.ID, issue.Title)
		fmt.Printf("   Status: %s, Last updated: %d days ago\n", issue.Status, daysStale)
		if issue.Assignee != "" {
			fmt.Printf("   Assignee: %s\n", issue.Assignee)
		}
		fmt.Println()
	}
}
func init() {
	staleCmd.Flags().IntP("days", "d", 30, "Issues not updated in this many days")
	staleCmd.Flags().StringP("status", "s", "", "Filter by status (open|in_progress|blocked)")
	staleCmd.Flags().IntP("limit", "n", 50, "Maximum issues to show")
	staleCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON format")
	rootCmd.AddCommand(staleCmd)
}
