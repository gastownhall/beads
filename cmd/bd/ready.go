package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var readyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show ready work (open, no active blockers)",
	Long: `Show ready work (open issues with no active blockers).

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

		// Handle --explain flag (dependency-aware reasoning)
		explain, _ := cmd.Flags().GetBool("explain")
		if explain {
			runReadyExplain(cmd)
			return
		}

		limit, _ := cmd.Flags().GetInt("limit")
		assignee, _ := cmd.Flags().GetString("assignee")
		unassigned, _ := cmd.Flags().GetBool("unassigned")
		sortPolicy, _ := cmd.Flags().GetString("sort")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		issueType, _ := cmd.Flags().GetString("type")
		issueType = utils.NormalizeIssueType(issueType) // Expand aliases (mr→merge-request, etc.)
		parentID, _ := cmd.Flags().GetString("parent")
		molTypeStr, _ := cmd.Flags().GetString("mol-type")
		prettyFormat, _ := cmd.Flags().GetBool("pretty")
		plainFormat, _ := cmd.Flags().GetBool("plain")
		includeDeferred, _ := cmd.Flags().GetBool("include-deferred")
		includeEphemeral, _ := cmd.Flags().GetBool("include-ephemeral")
		excludeTypeStrs, _ := cmd.Flags().GetStringSlice("exclude-type")
		var molType *types.MolType
		if molTypeStr != "" {
			mt := types.MolType(molTypeStr)
			if !mt.IsValid() {
				FatalError("invalid mol-type %q (must be swarm, patrol, or work)", molTypeStr)
			}
			molType = &mt
		}
		// Use global jsonOutput set by PersistentPreRun (respects config.yaml + env vars)

		// Normalize labels: trim, dedupe, remove empty
		labels = utils.NormalizeLabels(labels)
		labelsAny = utils.NormalizeLabels(labelsAny)

		// Apply directory-aware label scoping if no labels explicitly provided (GH#541)
		if len(labels) == 0 && len(labelsAny) == 0 {
			if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
				labelsAny = dirLabels
			}
		}

		// Normalize --exclude-type values.
		var excludeTypes []types.IssueType
		for _, raw := range excludeTypeStrs {
			for _, t := range strings.Split(raw, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					excludeTypes = append(excludeTypes, types.IssueType(utils.NormalizeIssueType(t)))
				}
			}
		}
		filter := types.WorkFilter{
			Status:           "open", // Only show open issues, not in_progress (matches bd list --ready)
			Type:             issueType,
			Limit:            limit,
			Unassigned:       unassigned,
			SortPolicy:       types.SortPolicy(sortPolicy),
			Labels:           labels,
			LabelsAny:        labelsAny,
			IncludeDeferred:  includeDeferred,  // GH#820: respect --include-deferred flag
			IncludeEphemeral: includeEphemeral, // bd-i5k5x: allow ephemeral issues (e.g., merge-requests)
			ExcludeTypes:     excludeTypes,
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

		// Metadata filters (GH#1406)
		metadataFieldFlags, _ := cmd.Flags().GetStringArray("metadata-field")
		if len(metadataFieldFlags) > 0 {
			filter.MetadataFields = make(map[string]string, len(metadataFieldFlags))
			for _, mf := range metadataFieldFlags {
				k, v, ok := strings.Cut(mf, "=")
				if !ok || k == "" {
					fmt.Fprintf(os.Stderr, "Error: invalid --metadata-field: expected key=value, got %q\n", mf)
					os.Exit(1)
				}
				if err := storage.ValidateMetadataKey(k); err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --metadata-field key: %v\n", err)
					os.Exit(1)
				}
				filter.MetadataFields[k] = v
			}
		}
		hasMetadataKey, _ := cmd.Flags().GetString("has-metadata-key")
		if hasMetadataKey != "" {
			if err := storage.ValidateMetadataKey(hasMetadataKey); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --has-metadata-key: %v\n", err)
				os.Exit(1)
			}
			filter.HasMetadataKey = hasMetadataKey
		}

		// Validate sort policy
		if !filter.SortPolicy.IsValid() {
			FatalError("invalid sort policy '%s'. Valid values: hybrid, priority, oldest", sortPolicy)
		}
		// Direct mode
		ctx := rootCtx

		activeStore := store
		// Contributor auto-routing: read from the same target repo as bd create.
		routedStore, routed, err := openRoutedReadStore(ctx, activeStore)
		if err != nil {
			FatalError("%v", err)
		}
		if routed {
			defer func() { _ = routedStore.Close() }()
			activeStore = routedStore
		}

		issues, err := activeStore.GetReadyWork(ctx, filter)
		if err != nil {
			FatalError("%v", err)
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
			// Best effort: display gracefully degrades with empty data
			labelsMap, _ := activeStore.GetLabelsForIssues(ctx, issueIDs)
			depCounts, _ := activeStore.GetDependencyCounts(ctx, issueIDs)
			allDeps, _ := activeStore.GetDependencyRecordsForIssues(ctx, issueIDs)
			commentCounts, _ := activeStore.GetCommentCounts(ctx, issueIDs)

			// Populate labels and dependencies for JSON output
			for _, issue := range issues {
				issue.Labels = labelsMap[issue.ID]
				issue.Dependencies = allDeps[issue.ID]
			}

			// Build response with counts + computed parent (consistent with bd list --json)
			issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
			for i, issue := range issues {
				counts := depCounts[issue.ID]
				if counts == nil {
					counts = &types.DependencyCounts{DependencyCount: 0, DependentCount: 0}
				}
				// Compute parent from dependency records
				var parent *string
				for _, dep := range allDeps[issue.ID] {
					if dep.Type == types.DepParentChild {
						parent = &dep.DependsOnID
						break
					}
				}
				issuesWithCounts[i] = &types.IssueWithCounts{
					Issue:           issue,
					DependencyCount: counts.DependencyCount,
					DependentCount:  counts.DependentCount,
					CommentCount:    commentCounts[issue.ID],
					Parent:          parent,
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
			if stats, statsErr := activeStore.GetStatistics(ctx); statsErr == nil {
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
		// Check if results were truncated by the limit
		totalReady := len(issues)
		truncated := false
		if filter.Limit > 0 && len(issues) == filter.Limit {
			// Re-query without limit to get total count
			countFilter := filter
			countFilter.Limit = 0
			allIssues, countErr := activeStore.GetReadyWork(ctx, countFilter)
			if countErr == nil && len(allIssues) > len(issues) {
				totalReady = len(allIssues)
				truncated = true
			}
		}

		// Build parent epic map for pretty display
		parentEpicMap := buildParentEpicMap(ctx, activeStore, issues)

		// Determine display mode: --plain or --pretty=false triggers plain format
		usePlain := plainFormat || !prettyFormat
		if usePlain {
			fmt.Printf("\n%s Ready work (%d issues with no active blockers):\n\n", ui.RenderAccent("📋"), len(issues))
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
		} else {
			displayReadyList(issues, parentEpicMap)
		}

		// Show truncation footer if results were limited
		if truncated {
			fmt.Printf("%s\n\n", ui.RenderMuted(fmt.Sprintf("Showing %d of %d ready issues. Use -n to show more.", len(issues), totalReady)))
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
		// Use factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
		ctx := rootCtx
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

// buildParentEpicMap builds a map from child issue ID to parent epic title.
// Only includes parents that are epics.
func buildParentEpicMap(ctx context.Context, s storage.DoltStorage, issues []*types.Issue) map[string]string {
	if len(issues) == 0 {
		return nil
	}
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}
	allDeps, err := s.GetDependencyRecordsForIssues(ctx, issueIDs)
	if err != nil {
		return nil
	}

	// Find parent-child deps where the issue is the child
	parentIDs := make(map[string]bool)
	childToParent := make(map[string]string) // childID -> parentID
	for issueID, deps := range allDeps {
		for _, dep := range deps {
			if dep.Type == types.DepParentChild {
				parentIDs[dep.DependsOnID] = true
				childToParent[issueID] = dep.DependsOnID
			}
		}
	}

	if len(parentIDs) == 0 {
		return nil
	}

	// Fetch parent issues and filter to epics
	epicTitles := make(map[string]string) // parentID -> title
	for parentID := range parentIDs {
		parent, err := s.GetIssue(ctx, parentID)
		if err != nil || parent == nil {
			continue
		}
		if parent.IssueType == "epic" {
			epicTitles[parentID] = parent.Title
		}
	}

	// Build final map: childID -> epic title
	result := make(map[string]string)
	for childID, parentID := range childToParent {
		if title, ok := epicTitles[parentID]; ok {
			result[childID] = title
		}
	}
	return result
}

// displayReadyList displays ready issues in pretty format with optional parent epic context
func displayReadyList(issues []*types.Issue, parentEpicMap map[string]string) {
	for _, issue := range issues {
		epicTitle := ""
		if parentEpicMap != nil {
			epicTitle = parentEpicMap[issue.ID]
		}
		fmt.Println(formatPrettyIssueWithContext(issue, epicTitle))
	}

	// Summary footer
	fmt.Println()
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("Ready: %d issues with no active blockers\n", len(issues))
	fmt.Println()
	fmt.Println("Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred")
}

// runReadyExplain shows dependency-aware reasoning for why issues are ready or blocked.
func runReadyExplain(cmd *cobra.Command) {
	ctx := rootCtx

	// Get the active store (respect --rig flag)
	activeStore := store
	rigOverride, _ := cmd.Flags().GetString("rig")
	if rigOverride != "" {
		rigStore, err := openStoreForRig(ctx, rigOverride)
		if err != nil {
			FatalError("%v", err)
		}
		defer func() { _ = rigStore.Close() }()
		activeStore = rigStore
	} else {
		routedStore, routed, err := openRoutedReadStore(ctx, activeStore)
		if err != nil {
			FatalError("%v", err)
		}
		if routed {
			defer func() { _ = routedStore.Close() }()
			activeStore = routedStore
		}
	}

	// Get ready issues (no limit for explain mode — show everything)
	filter := types.WorkFilter{
		Status:     types.StatusOpen,
		SortPolicy: types.SortPolicyPriority,
	}
	readyIssues, err := activeStore.GetReadyWork(ctx, filter)
	if err != nil {
		FatalErrorRespectJSON("%v", err)
	}

	// Get blocked issues
	blockedIssues, err := activeStore.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		FatalErrorRespectJSON("%v", err)
	}

	// Get dependency records for ready issues to find resolved blockers
	readyIDs := make([]string, len(readyIssues))
	for i, issue := range readyIssues {
		readyIDs[i] = issue.ID
	}
	depCounts, _ := activeStore.GetDependencyCounts(ctx, readyIDs)
	allDeps, _ := activeStore.GetDependencyRecordsForIssues(ctx, readyIDs)

	// Detect cycles
	cycles, _ := activeStore.DetectCycles(ctx)

	// Build ready items with explanations
	readyItems := make([]types.ReadyItem, 0, len(readyIssues))
	for _, issue := range readyIssues {
		counts := depCounts[issue.ID]
		if counts == nil {
			counts = &types.DependencyCounts{}
		}

		// Find resolved blockers (closed issues that this depended on)
		var resolvedBlockers []string
		reason := "no blocking dependencies"
		deps := allDeps[issue.ID]
		for _, dep := range deps {
			if dep.Type == types.DepBlocks || dep.Type == types.DepConditionalBlocks || dep.Type == types.DepWaitsFor {
				resolvedBlockers = append(resolvedBlockers, dep.DependsOnID)
			}
		}
		if len(resolvedBlockers) > 0 {
			reason = fmt.Sprintf("%d blocker(s) resolved", len(resolvedBlockers))
		}

		// Compute parent
		var parent *string
		for _, dep := range deps {
			if dep.Type == types.DepParentChild {
				parent = &dep.DependsOnID
				break
			}
		}

		readyItems = append(readyItems, types.ReadyItem{
			Issue:            issue,
			Reason:           reason,
			ResolvedBlockers: resolvedBlockers,
			DependencyCount:  counts.DependencyCount,
			DependentCount:   counts.DependentCount,
			Parent:           parent,
		})
	}

	// Build blocked items with blocker details
	blockedItems := make([]types.BlockedItem, 0, len(blockedIssues))
	// Collect all blocker IDs to batch-fetch
	allBlockerIDs := make(map[string]bool)
	for _, bi := range blockedIssues {
		for _, blockerID := range bi.BlockedBy {
			allBlockerIDs[blockerID] = true
		}
	}
	blockerIDList := make([]string, 0, len(allBlockerIDs))
	for id := range allBlockerIDs {
		blockerIDList = append(blockerIDList, id)
	}
	blockerIssues, _ := activeStore.GetIssuesByIDs(ctx, blockerIDList)
	blockerMap := make(map[string]*types.Issue, len(blockerIssues))
	for _, issue := range blockerIssues {
		blockerMap[issue.ID] = issue
	}

	for _, bi := range blockedIssues {
		blockers := make([]types.BlockerInfo, 0, len(bi.BlockedBy))
		for _, blockerID := range bi.BlockedBy {
			info := types.BlockerInfo{ID: blockerID}
			if blocker, ok := blockerMap[blockerID]; ok {
				info.Title = blocker.Title
				info.Status = blocker.Status
				info.Priority = blocker.Priority
			}
			blockers = append(blockers, info)
		}
		blockedItems = append(blockedItems, types.BlockedItem{
			Issue:          bi.Issue,
			BlockedBy:      blockers,
			BlockedByCount: bi.BlockedByCount,
		})
	}

	// Build cycle info
	var cycleIDs [][]string
	for _, cycle := range cycles {
		ids := make([]string, len(cycle))
		for i, issue := range cycle {
			ids[i] = issue.ID
		}
		cycleIDs = append(cycleIDs, ids)
	}

	explanation := types.ReadyExplanation{
		Ready:   readyItems,
		Blocked: blockedItems,
		Cycles:  cycleIDs,
		Summary: types.ExplainSummary{
			TotalReady:   len(readyItems),
			TotalBlocked: len(blockedItems),
			CycleCount:   len(cycleIDs),
		},
	}

	if jsonOutput {
		outputJSON(explanation)
		return
	}

	// Human-readable output
	fmt.Printf("\n%s Ready Work Explanation\n\n", ui.RenderAccent("📊"))

	// Ready section
	if len(explanation.Ready) > 0 {
		fmt.Printf("%s Ready (%d issues):\n\n", ui.RenderPass("●"), len(explanation.Ready))
		for _, item := range explanation.Ready {
			fmt.Printf("  %s [%s] %s\n",
				ui.RenderID(item.ID),
				ui.RenderPriority(item.Priority),
				item.Title)
			fmt.Printf("    Reason: %s\n", item.Reason)
			if len(item.ResolvedBlockers) > 0 {
				fmt.Printf("    Resolved blockers: %s\n", strings.Join(item.ResolvedBlockers, ", "))
			}
			if item.DependentCount > 0 {
				fmt.Printf("    Unblocks: %d issue(s)\n", item.DependentCount)
			}
			fmt.Println()
		}
	} else {
		fmt.Printf("%s No ready work\n\n", ui.RenderWarn("○"))
	}

	// Blocked section
	if len(explanation.Blocked) > 0 {
		fmt.Printf("%s Blocked (%d issues):\n\n", ui.RenderFail("●"), len(explanation.Blocked))
		for _, item := range explanation.Blocked {
			fmt.Printf("  %s [%s] %s\n",
				ui.RenderID(item.ID),
				ui.RenderPriority(item.Priority),
				item.Title)
			for _, blocker := range item.BlockedBy {
				fmt.Printf("    ← blocked by %s: %s [%s]\n",
					ui.RenderID(blocker.ID), blocker.Title, blocker.Status)
			}
			fmt.Println()
		}
	}

	// Cycles section
	if len(explanation.Cycles) > 0 {
		fmt.Printf("%s Cycles detected (%d):\n\n", ui.RenderFail("⚠"), len(explanation.Cycles))
		for _, cycle := range explanation.Cycles {
			fmt.Printf("  %s → %s\n", strings.Join(cycle, " → "), cycle[0])
		}
		fmt.Println()
	}

	// Summary
	fmt.Printf("%s Summary: %d ready, %d blocked",
		ui.RenderMuted("─"),
		explanation.Summary.TotalReady,
		explanation.Summary.TotalBlocked)
	if explanation.Summary.CycleCount > 0 {
		fmt.Printf(", %d cycle(s)", explanation.Summary.CycleCount)
	}
	fmt.Printf("\n\n")
}

// runMoleculeReady shows ready steps within a specific molecule
func runMoleculeReady(_ *cobra.Command, molIDArg string) {
	ctx := rootCtx

	// Molecule-ready requires direct store access for subgraph loading
	if store == nil {
		FatalError("no database connection")
	}

	// Resolve molecule ID
	moleculeID, err := utils.ResolvePartialID(ctx, store, molIDArg)
	if err != nil {
		FatalError("molecule '%s' not found", molIDArg)
	}

	// Load molecule subgraph
	subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
	if err != nil {
		FatalError("loading molecule: %v", err)
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
	readyCmd.Flags().StringP("sort", "s", "priority", "Sort policy: priority (default), hybrid, oldest")
	readyCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	readyCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	readyCmd.Flags().StringP("type", "t", "", "Filter by issue type (task, bug, feature, epic, decision, merge-request). Aliases: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision")
	readyCmd.Flags().String("mol", "", "Filter to steps within a specific molecule")
	readyCmd.Flags().String("parent", "", "Filter to descendants of this bead/epic")
	readyCmd.Flags().String("mol-type", "", "Filter by molecule type: swarm, patrol, or work")
	readyCmd.Flags().Bool("pretty", true, "Display issues in a tree format with status/priority symbols")
	readyCmd.Flags().Bool("plain", false, "Display issues as a plain numbered list")
	readyCmd.Flags().Bool("include-deferred", false, "Include issues with future defer_until timestamps")
	readyCmd.Flags().Bool("include-ephemeral", false, "Include ephemeral issues (wisps) in results")
	readyCmd.Flags().Bool("gated", false, "Find molecules ready for gate-resume dispatch")
	readyCmd.Flags().StringSlice("exclude-type", nil, "Exclude issue types from results (comma-separated or repeatable, e.g., --exclude-type=convoy,epic)")
	readyCmd.Flags().String("rig", "", "Query a different rig's database (e.g., --rig my-project, --rig gt-, --rig gt)")
	readyCmd.Flags().Bool("explain", false, "Show dependency-aware reasoning for why issues are ready or blocked")
	// Metadata filtering (GH#1406)
	readyCmd.Flags().StringArray("metadata-field", nil, "Filter by metadata field (key=value, repeatable)")
	readyCmd.Flags().String("has-metadata-key", "", "Filter issues that have this metadata key set")
	rootCmd.AddCommand(readyCmd)
	blockedCmd.Flags().String("parent", "", "Filter to descendants of this bead/epic")
	rootCmd.AddCommand(blockedCmd)
}
