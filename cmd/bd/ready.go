package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/util"
	"github.com/steveyegge/beads/internal/utils"
)

var readyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show ready work (open, no blockers)",
	Long: `Show ready work (open issues with no blockers).

Excludes in_progress, blocked, deferred, and hooked issues. This uses the
GetReadyWork API which applies blocker-aware semantics to find truly claimable work.

Note: 'bd list --ready' is NOT equivalent - it only filters by status=open.

Use --mol to filter to a specific molecule's steps:
  bd ready --mol bd-patrol   # Show ready steps within molecule

Use --gated to find molecules ready for gate-resume dispatch:
  bd ready --gated           # Find molecules where a gate closed

This is useful for agents executing molecules to see which steps can run next.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Handle --gated flag (gate-resume discovery)
		gated, _ := cmd.Flags().GetBool("gated")
		if gated {
			runMolReadyGated(cmd, args)
			return
		}

		// Handle molecule-specific ready query
		molID, _ := cmd.Flags().GetString("mol")
		if molID != "" {
			runMoleculeReady(cmd, molID)
			return
		}

		limit, _ := cmd.Flags().GetInt("limit")
		assignee, _ := cmd.Flags().GetString("assignee")
		unassigned, _ := cmd.Flags().GetBool("unassigned")
		sortPolicy, _ := cmd.Flags().GetString("sort")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		issueType, _ := cmd.Flags().GetString("type")
		issueType = util.NormalizeIssueType(issueType) // Expand aliases (mr→merge-request, etc.)
		parentID, _ := cmd.Flags().GetString("parent")
		molTypeStr, _ := cmd.Flags().GetString("mol-type")
		prettyFormat, _ := cmd.Flags().GetBool("pretty")
		includeDeferred, _ := cmd.Flags().GetBool("include-deferred")
		var molType *types.MolType
		if molTypeStr != "" {
			mt := types.MolType(molTypeStr)
			if !mt.IsValid() {
				fmt.Fprintf(os.Stderr, "Error: invalid mol-type %q (must be swarm, patrol, or work)\n", molTypeStr)
				os.Exit(1)
			}
			molType = &mt
		}
		// Use global jsonOutput set by PersistentPreRun (respects config.yaml + env vars)

		// Normalize labels: trim, dedupe, remove empty
		labels = util.NormalizeLabels(labels)
		labelsAny = util.NormalizeLabels(labelsAny)

		// Apply directory-aware label scoping if no labels explicitly provided (GH#541)
		if len(labels) == 0 && len(labelsAny) == 0 {
			if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
				labelsAny = dirLabels
			}
		}

		filter := types.WorkFilter{
			Status:          "open", // Only show open issues, not in_progress (matches bd list --ready)
			Type:            issueType,
			Limit:           limit,
			Unassigned:      unassigned,
			SortPolicy:      types.SortPolicy(sortPolicy),
			Labels:          labels,
			LabelsAny:       labelsAny,
			IncludeDeferred: includeDeferred, // GH#820: respect --include-deferred flag
		}
		// Use Changed() to properly handle P0 (priority=0)
		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			filter.Priority = &priority
		}
		if assignee != "" && !unassigned {
			filter.Assignee = &assignee
		}
		if parentID != "" {
			filter.ParentID = &parentID
		}
		if molType != nil {
			filter.MolType = molType
		}
		// Validate sort policy
		if !filter.SortPolicy.IsValid() {
			fmt.Fprintf(os.Stderr, "Error: invalid sort policy '%s'. Valid values: hybrid, priority, oldest\n", sortPolicy)
			os.Exit(1)
		}
		// If daemon is running, use RPC
		if daemonClient != nil {
			readyArgs := &rpc.ReadyArgs{
				Status:          "open", // Only show open issues, not in_progress (matches bd list --ready)
				Assignee:        assignee,
				Unassigned:      unassigned,
				Type:            issueType,
				Limit:           limit,
				SortPolicy:      sortPolicy,
				Labels:          labels,
				LabelsAny:       labelsAny,
				ParentID:        parentID,
				MolType:         molTypeStr,
				IncludeDeferred: includeDeferred, // GH#820
			}
			if cmd.Flags().Changed("priority") {
				priority, _ := cmd.Flags().GetInt("priority")
				readyArgs.Priority = &priority
			}
			resp, err := daemonClient.Ready(readyArgs)
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

			// Show upgrade notification if needed
			maybeShowUpgradeNotification()

			if len(issues) == 0 {
				// Check if there are any open issues at all
				statsResp, statsErr := daemonClient.Stats()
				hasOpenIssues := false
				if statsErr == nil {
					var stats types.Statistics
					if json.Unmarshal(statsResp.Data, &stats) == nil {
						hasOpenIssues = stats.OpenIssues > 0 || stats.InProgressIssues > 0
					}
				}
				if hasOpenIssues {
					fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
						ui.RenderWarn("✨"))
				} else {
					fmt.Printf("\n%s No open issues\n\n", ui.RenderPass("✨"))
				}
				return
			}
			if prettyFormat {
				displayPrettyList(issues, false)
			} else {
				fmt.Printf("\n%s Ready work (%d issues with no blockers):\n\n", ui.RenderAccent("📋"), len(issues))
				for i, issue := range issues {
					fmt.Printf("%d. [%s] [%s] %s: %s\n", i+1,
						ui.RenderPriority(issue.Priority),
						ui.RenderType(string(issue.IssueType)),
						ui.RenderID(issue.ID), issue.Title)
					if issue.EstimatedMinutes != nil {
						fmt.Printf("   Estimate: %d min\n", *issue.EstimatedMinutes)
					}
					if issue.Assignee != "" {
						fmt.Printf("   Assignee: %s\n", issue.Assignee)
					}
				}
				fmt.Println()
			}
			return
		}
		// Direct mode
		ctx := rootCtx

		// Check database freshness before reading
		// Skip check when using daemon (daemon auto-imports on staleness)
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		issues, err := store.GetReadyWork(ctx, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		// If no ready work found, check if git has issues and auto-import
		if len(issues) == 0 {
			if checkAndAutoImport(ctx, store) {
				// Re-run the query after import
				issues, err = store.GetReadyWork(ctx, filter)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			}
		}
		if jsonOutput {
			// Always output array, even if empty
			if issues == nil {
				issues = []*types.Issue{}
			}
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)
			issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
			for i, issue := range issues {
				issuesWithCounts[i] = &types.IssueWithCounts{
					Issue:        issue,
					CommentCount: commentCounts[issue.ID],
				}
			}
			outputJSON(issuesWithCounts)
			return
		}
		// Show upgrade notification if needed
		maybeShowUpgradeNotification()

		if len(issues) == 0 {
			// Check if there are any open issues at all
			hasOpenIssues := false
			if stats, statsErr := store.GetStatistics(ctx); statsErr == nil {
				hasOpenIssues = stats.OpenIssues > 0 || stats.InProgressIssues > 0
			}
			if hasOpenIssues {
				fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
					ui.RenderWarn("✨"))
			} else {
				fmt.Printf("\n%s No open issues\n\n", ui.RenderPass("✨"))
			}
			// Show tip even when no ready work found
			maybeShowTip(store)
			return
		}
		if prettyFormat {
			displayPrettyList(issues, false)
		} else {
			fmt.Printf("\n%s Ready work (%d issues with no blockers):\n\n", ui.RenderAccent("📋"), len(issues))
			for i, issue := range issues {
				fmt.Printf("%d. [%s] [%s] %s: %s\n", i+1,
					ui.RenderPriority(issue.Priority),
					ui.RenderType(string(issue.IssueType)),
					ui.RenderID(issue.ID), issue.Title)
				if issue.EstimatedMinutes != nil {
					fmt.Printf("   Estimate: %d min\n", *issue.EstimatedMinutes)
				}
				if issue.Assignee != "" {
					fmt.Printf("   Assignee: %s\n", issue.Assignee)
				}
			}
			fmt.Println()
		}

		// Show tip after successful ready (direct mode only)
		maybeShowTip(store)
	},
}
var blockedCmd = &cobra.Command{
	Use:   "blocked",
	Short: "Show blocked issues",
	Run: func(cmd *cobra.Command, args []string) {
		// Use global jsonOutput set by PersistentPreRun (respects config.yaml + env vars)
		// If daemon is running but doesn't support this command, use direct storage
		// Use factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
		ctx := rootCtx
		if daemonClient != nil && store == nil {
			var err error
			store, err = factory.NewFromConfig(ctx, filepath.Dir(dbPath))
			if err != nil {
				FatalErrorRespectJSON("failed to open database: %v", err)
			}
			defer func() { _ = store.Close() }()
		}
		parentID, _ := cmd.Flags().GetString("parent")
		var blockedFilter types.WorkFilter
		if parentID != "" {
			blockedFilter.ParentID = &parentID
		}
		blocked, err := store.GetBlockedIssues(ctx, blockedFilter)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			// Always output array, even if empty
			if blocked == nil {
				blocked = []*types.BlockedIssue{}
			}
			outputJSON(blocked)
			return
		}
		if len(blocked) == 0 {
			fmt.Printf("\n%s No blocked issues\n\n", ui.RenderPass("✨"))
			return
		}
		fmt.Printf("\n%s Blocked issues (%d):\n\n", ui.RenderFail("🚫"), len(blocked))
		for _, issue := range blocked {
			fmt.Printf("[%s] %s: %s\n",
				ui.RenderPriority(issue.Priority),
				ui.RenderID(issue.ID), issue.Title)
			blockedBy := issue.BlockedBy
			if blockedBy == nil {
				blockedBy = []string{}
			}
			fmt.Printf("  Blocked by %d open dependencies: %v\n",
				issue.BlockedByCount, blockedBy)
			fmt.Println()
		}
	},
}

// runMoleculeReady shows ready steps within a specific molecule
func runMoleculeReady(_ *cobra.Command, molIDArg string) {
	ctx := rootCtx

	// Molecule-ready requires direct store access for subgraph loading
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: bd ready --mol requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon ready --mol %s\n", molIDArg)
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	// Resolve molecule ID
	moleculeID, err := utils.ResolvePartialID(ctx, store, molIDArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: molecule '%s' not found\n", molIDArg)
		os.Exit(1)
	}

	// Load molecule subgraph
	subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
		os.Exit(1)
	}

	// Get parallel analysis to find ready steps
	analysis := analyzeMoleculeParallel(subgraph)

	// Collect ready steps
	var readySteps []*MoleculeReadyStep
	for _, issue := range subgraph.Issues {
		info := analysis.Steps[issue.ID]
		if info != nil && info.IsReady {
			readySteps = append(readySteps, &MoleculeReadyStep{
				Issue:         issue,
				ParallelInfo:  info,
				ParallelGroup: info.ParallelGroup,
			})
		}
	}

	if jsonOutput {
		output := MoleculeReadyOutput{
			MoleculeID:     moleculeID,
			MoleculeTitle:  subgraph.Root.Title,
			TotalSteps:     analysis.TotalSteps,
			ReadySteps:     len(readySteps),
			Steps:          readySteps,
			ParallelGroups: analysis.ParallelGroups,
		}
		outputJSON(output)
		return
	}

	// Human-readable output
	fmt.Printf("\n%s Ready steps in molecule: %s\n", ui.RenderAccent("🧪"), subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", moleculeID)
	fmt.Printf("   Total: %d steps, %d ready\n", analysis.TotalSteps, len(readySteps))

	if len(readySteps) == 0 {
		fmt.Printf("\n%s No ready steps (all blocked or completed)\n\n", ui.RenderWarn("✨"))
		return
	}

	// Show parallel groups if any
	if len(analysis.ParallelGroups) > 0 {
		fmt.Printf("\n%s Parallel Groups:\n", ui.RenderPass("⚡"))
		for groupName, members := range analysis.ParallelGroups {
			// Check if any members are ready
			readyInGroup := 0
			for _, id := range members {
				if info := analysis.Steps[id]; info != nil && info.IsReady {
					readyInGroup++
				}
			}
			if readyInGroup > 0 {
				fmt.Printf("   %s: %d ready\n", groupName, readyInGroup)
			}
		}
	}

	fmt.Printf("\n%s Ready steps:\n\n", ui.RenderPass("📋"))
	for i, step := range readySteps {
		// Show parallel group if in one
		groupAnnotation := ""
		if step.ParallelGroup != "" {
			groupAnnotation = fmt.Sprintf(" [%s]", ui.RenderAccent(step.ParallelGroup))
		}

		fmt.Printf("%d. [%s] [%s] %s: %s%s\n", i+1,
			ui.RenderPriority(step.Issue.Priority),
			ui.RenderType(string(step.Issue.IssueType)),
			ui.RenderID(step.Issue.ID),
			step.Issue.Title,
			groupAnnotation)

		// Show what this step can parallelize with
		if len(step.ParallelInfo.CanParallel) > 0 {
			readyParallel := []string{}
			for _, pID := range step.ParallelInfo.CanParallel {
				if pInfo := analysis.Steps[pID]; pInfo != nil && pInfo.IsReady {
					readyParallel = append(readyParallel, pID)
				}
			}
			if len(readyParallel) > 0 {
				fmt.Printf("   Can run with: %v\n", readyParallel)
			}
		}
	}
	fmt.Println()
}

// MoleculeReadyStep holds a ready step with its parallel info
type MoleculeReadyStep struct {
	Issue         *types.Issue  `json:"issue"`
	ParallelInfo  *ParallelInfo `json:"parallel_info"`
	ParallelGroup string        `json:"parallel_group,omitempty"`
}

// MoleculeReadyOutput is the JSON output for bd ready --mol
type MoleculeReadyOutput struct {
	MoleculeID     string               `json:"molecule_id"`
	MoleculeTitle  string               `json:"molecule_title"`
	TotalSteps     int                  `json:"total_steps"`
	ReadySteps     int                  `json:"ready_steps"`
	Steps          []*MoleculeReadyStep `json:"steps"`
	ParallelGroups map[string][]string  `json:"parallel_groups"`
}

func init() {
	readyCmd.Flags().IntP("limit", "n", 10, "Maximum issues to show")
	readyCmd.Flags().IntP("priority", "p", 0, "Filter by priority")
	readyCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	readyCmd.Flags().BoolP("unassigned", "u", false, "Show only unassigned issues")
	readyCmd.Flags().StringP("sort", "s", "hybrid", "Sort policy: hybrid (default), priority, oldest")
	readyCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	readyCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	readyCmd.Flags().StringP("type", "t", "", "Filter by issue type (task, bug, feature, epic, merge-request). Aliases: mr→merge-request, feat→feature, mol→molecule")
	readyCmd.Flags().String("mol", "", "Filter to steps within a specific molecule")
	readyCmd.Flags().String("parent", "", "Filter to descendants of this bead/epic")
	readyCmd.Flags().String("mol-type", "", "Filter by molecule type: swarm, patrol, or work")
	readyCmd.Flags().Bool("pretty", false, "Display issues in a tree format with status/priority symbols")
	readyCmd.Flags().Bool("include-deferred", false, "Include issues with future defer_until timestamps")
	readyCmd.Flags().Bool("gated", false, "Find molecules ready for gate-resume dispatch")
	rootCmd.AddCommand(readyCmd)
	blockedCmd.Flags().String("parent", "", "Filter to descendants of this bead/epic")
	rootCmd.AddCommand(blockedCmd)
}
