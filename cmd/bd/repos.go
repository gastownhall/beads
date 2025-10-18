package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
)

var reposCmd = &cobra.Command{
	Use:   "repos",
	Short: "Multi-repository management (requires global daemon)",
	Long: `Manage work across multiple repositories when using a global daemon.

This command requires a running global daemon (bd daemon --global).
It allows you to view and aggregate work across all cached repositories.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var reposListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all cached repositories",
	Long: `Show all repositories that the daemon has cached.

The daemon caches a repository after any command is run from that directory.
This command shows all active caches with their paths, prefixes, and issue counts.`,
	Run: func(cmd *cobra.Command, args []string) {
		if daemonClient == nil {
			fmt.Fprintf(os.Stderr, "Error: This command requires a running daemon\n")
			fmt.Fprintf(os.Stderr, "Start one with: bd daemon --global\n")
			os.Exit(1)
		}

		resp, err := daemonClient.ReposList()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		var repos []rpc.RepoInfo
		if err := json.Unmarshal(resp.Data, &repos); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(repos)
			return
		}

		if len(repos) == 0 {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Printf("\n%s No repositories cached yet\n", yellow("📁"))
			fmt.Printf("Repositories are cached when you run commands from their directories.\n\n")
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("\n%s Cached Repositories (%d):\n\n", cyan("📁"), len(repos))

		for _, repo := range repos {
			prefix := repo.Prefix
			if prefix == "" {
				prefix = "(no prefix)"
			}
			fmt.Printf("%s\n", repo.Path)
			fmt.Printf("  Prefix:       %s\n", prefix)
			fmt.Printf("  Issue Count:  %s\n", green(fmt.Sprintf("%d", repo.IssueCount)))
			fmt.Printf("  Status:       %s\n", repo.LastAccess)
			fmt.Println()
		}
	},
}

var reposReadyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show ready work across all repositories",
	Long: `Display ready work (issues with no blockers) from all cached repositories.

By default, shows a flat list of all ready work. Use --group to organize by repository.`,
	Run: func(cmd *cobra.Command, args []string) {
		if daemonClient == nil {
			fmt.Fprintf(os.Stderr, "Error: This command requires a running daemon\n")
			fmt.Fprintf(os.Stderr, "Start one with: bd daemon --global\n")
			os.Exit(1)
		}

		limit, _ := cmd.Flags().GetInt("limit")
		assignee, _ := cmd.Flags().GetString("assignee")
		groupByRepo, _ := cmd.Flags().GetBool("group")

		readyArgs := &rpc.ReposReadyArgs{
			Assignee:    assignee,
			Limit:       limit,
			GroupByRepo: groupByRepo,
		}

		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			readyArgs.Priority = &priority
		}

		resp, err := daemonClient.ReposReady(readyArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if groupByRepo {
			var grouped []rpc.RepoReadyWork
			if err := json.Unmarshal(resp.Data, &grouped); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				outputJSON(grouped)
				return
			}

			if len(grouped) == 0 {
				yellow := color.New(color.FgYellow).SprintFunc()
				fmt.Printf("\n%s No ready work found across any repositories\n\n", yellow("✨"))
				return
			}

			cyan := color.New(color.FgCyan).SprintFunc()
			fmt.Printf("\n%s Ready work across %d repositories:\n\n", cyan("📋"), len(grouped))

			for _, repo := range grouped {
				fmt.Printf("%s (%d issues):\n", repo.RepoPath, len(repo.Issues))
				for i, issue := range repo.Issues {
					fmt.Printf("  %d. [P%d] %s: %s\n", i+1, issue.Priority, issue.ID, issue.Title)
					if issue.EstimatedMinutes != nil {
						fmt.Printf("     Estimate: %d min\n", *issue.EstimatedMinutes)
					}
					if issue.Assignee != "" {
						fmt.Printf("     Assignee: %s\n", issue.Assignee)
					}
				}
				fmt.Println()
			}
		} else {
			var issues []rpc.ReposReadyIssue
			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				outputJSON(issues)
				return
			}

			if len(issues) == 0 {
				yellow := color.New(color.FgYellow).SprintFunc()
				fmt.Printf("\n%s No ready work found across any repositories\n\n", yellow("✨"))
				return
			}

			cyan := color.New(color.FgCyan).SprintFunc()
			fmt.Printf("\n%s Ready work across all repositories (%d issues):\n\n", cyan("📋"), len(issues))

			for i, item := range issues {
				issue := item.Issue
				fmt.Printf("%d. [P%d] %s: %s\n", i+1, issue.Priority, issue.ID, issue.Title)
				fmt.Printf("   Repo: %s\n", item.RepoPath)

				if issue.EstimatedMinutes != nil {
					fmt.Printf("   Estimate: %d min\n", *issue.EstimatedMinutes)
				}
				if issue.Assignee != "" {
					fmt.Printf("   Assignee: %s\n", issue.Assignee)
				}
			}
			fmt.Println()
		}
	},
}

var reposStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show combined statistics across all repositories",
	Long: `Display aggregated statistics from all cached repositories.

Shows both total combined statistics and per-repository breakdowns.`,
	Run: func(cmd *cobra.Command, args []string) {
		if daemonClient == nil {
			fmt.Fprintf(os.Stderr, "Error: This command requires a running daemon\n")
			fmt.Fprintf(os.Stderr, "Start one with: bd daemon --global\n")
			os.Exit(1)
		}

		resp, err := daemonClient.ReposStats()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		var statsResp rpc.ReposStatsResponse
		if err := json.Unmarshal(resp.Data, &statsResp); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(statsResp)
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		green := color.New(color.FgGreen).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()

		fmt.Printf("\n%s Combined Statistics Across All Repositories:\n\n", cyan("📊"))
		fmt.Printf("Total Issues:      %d\n", statsResp.Total.TotalIssues)
		fmt.Printf("Open:              %s\n", green(fmt.Sprintf("%d", statsResp.Total.OpenIssues)))
		fmt.Printf("In Progress:       %s\n", yellow(fmt.Sprintf("%d", statsResp.Total.InProgressIssues)))
		fmt.Printf("Closed:            %d\n", statsResp.Total.ClosedIssues)
		fmt.Printf("Blocked:           %d\n", statsResp.Total.BlockedIssues)
		fmt.Printf("Ready:             %s\n", green(fmt.Sprintf("%d", statsResp.Total.ReadyIssues)))
		fmt.Println()

		if len(statsResp.PerRepo) > 0 {
			fmt.Printf("%s Per-Repository Breakdown:\n\n", cyan("📁"))
			for path, stats := range statsResp.PerRepo {
				fmt.Printf("%s:\n", path)
				fmt.Printf("  Total: %d  Ready: %s  Blocked: %d\n",
					stats.TotalIssues, green(fmt.Sprintf("%d", stats.ReadyIssues)), stats.BlockedIssues)
				fmt.Println()
			}
		}

		if len(statsResp.Errors) > 0 {
			red := color.New(color.FgRed).SprintFunc()
			fmt.Printf("%s Errors (%d repositories):\n", red("⚠"), len(statsResp.Errors))
			for path, errMsg := range statsResp.Errors {
				fmt.Printf("  %s: %s\n", path, errMsg)
			}
			fmt.Println()
		}
	},
}

var reposClearCacheCmd = &cobra.Command{
	Use:   "clear-cache",
	Short: "Clear all cached repository connections",
	Long: `Close all cached storage connections and clear the daemon's repository cache.

Useful for freeing resources or forcing the daemon to reload repository databases.
The cache will be rebuilt automatically as commands are run from different directories.`,
	Run: func(cmd *cobra.Command, args []string) {
		if daemonClient == nil {
			fmt.Fprintf(os.Stderr, "Error: This command requires a running daemon\n")
			fmt.Fprintf(os.Stderr, "Start one with: bd daemon --global\n")
			os.Exit(1)
		}

		resp, err := daemonClient.ReposClearCache()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Println(string(resp.Data))
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("\n%s Repository cache cleared successfully\n\n", green("✅"))
	},
}

func init() {
	reposReadyCmd.Flags().IntP("limit", "n", 10, "Maximum issues to show per repository")
	reposReadyCmd.Flags().IntP("priority", "p", -1, "Filter by priority (0-4)")
	reposReadyCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	reposReadyCmd.Flags().BoolP("group", "g", false, "Group issues by repository")

	reposCmd.AddCommand(reposListCmd)
	reposCmd.AddCommand(reposReadyCmd)
	reposCmd.AddCommand(reposStatsCmd)
	reposCmd.AddCommand(reposClearCacheCmd)

	rootCmd.AddCommand(reposCmd)
}
