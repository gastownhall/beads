package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var staleCmd = &cobra.Command{
	Use:     "stale",
	GroupID: "views",
	Short:   "Show stale issues (not updated recently)",
	Long: `Show issues that haven't been updated recently and may need attention.
This helps identify:
- In-progress issues with no recent activity (may be abandoned)
- Open issues that have been forgotten
- Issues that might be outdated or no longer relevant`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("stale")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		days, _ := cmd.Flags().GetInt("days")
		status, _ := cmd.Flags().GetString("status")
		limit, _ := cmd.Flags().GetInt("limit")
		if days < 1 {
			return HandleErrorRespectJSON("--days must be at least 1")
		}
		if status != "" && status != "open" && status != "in_progress" && status != "blocked" && status != "deferred" {
			return HandleErrorRespectJSON("invalid status '%s'. Valid values: open, in_progress, blocked, deferred", status)
		}
		filter := types.StaleFilter{
			Days:   days,
			Status: status,
			Limit:  limit,
		}

		if usesProxiedServer() {
			return runStaleProxiedServer(rootCtx, filter)
		}

		issues, err := store.GetStaleIssues(rootCtx, filter)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		return renderStale(issues, filter.Days)
	},
}

func renderStale(issues []*types.Issue, days int) error {
	if jsonOutput {
		if issues == nil {
			issues = []*types.Issue{}
		}
		return outputJSON(issues)
	}
	displayStaleIssues(issues, days)
	return nil
}

func displayStaleIssues(issues []*types.Issue, days int) {
	if len(issues) == 0 {
		fmt.Printf("\n%s No stale issues found (all active)\n\n", ui.RenderPass("✨"))
		return
	}
	fmt.Printf("\n%s Stale issues (%d not updated in %d+ days):\n\n", ui.RenderWarn("⏰"), len(issues), days)
	now := time.Now()
	for i, issue := range issues {
		daysStale := int(now.Sub(issue.UpdatedAt).Hours() / 24)
		fmt.Printf("%d. [%s] %s: %s\n", i+1, ui.RenderPriority(issue.Priority), ui.RenderID(issue.ID), issue.Title)
		fmt.Printf("   Status: %s, Last updated: %d days ago\n", ui.RenderStatus(string(issue.Status)), daysStale)
		if issue.Assignee != "" {
			fmt.Printf("   Assignee: %s\n", issue.Assignee)
		}
		fmt.Println()
	}
}
func init() {
	staleCmd.Flags().IntP("days", "d", 30, "Issues not updated in this many days")
	staleCmd.Flags().StringP("status", "s", "", "Filter by status (open|in_progress|blocked|deferred)")
	staleCmd.Flags().IntP("limit", "n", 50, "Maximum issues to show")
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(staleCmd)
}
