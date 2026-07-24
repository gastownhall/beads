package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/metrics"
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

Note: 'bd list --ready' uses the same blocker-aware ready-work semantics.

Use --mol to filter to a specific molecule's steps:
  bd ready --mol bd-patrol   # Show ready steps within molecule

Use --gated to find molecules ready for gate-resume dispatch:
  bd ready --gated           # Find molecules where a gate closed

Use --claim to atomically claim the first ready issue matching the filters:
  bd ready --claim --json

This is useful for agents executing molecules to see which steps can run next.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("ready")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		claimReady, _ := cmd.Flags().GetBool("claim")

		if usesProxiedServer() {
			// --claim consumes exactly one row, same reasoning as the
			// direct-path fix in issueops/claim.go: a rig-wide cap sized
			// for bulk list/ready reads must not block a single-row claim.
			// Only the bulk (non-claim) proxied ready listing rejects an
			// active cap.
			if !claimReady {
				if err := rejectMaxRowsUnderProxiedServer(cmd); err != nil {
					return err
				}
			} else {
				// Still validate --max-rows/BEADS_MAX_ROWS here even though
				// the resolved cap is ignored below: resolveMaxRows is also
				// where a malformed value (e.g. --max-rows -1) is rejected
				// with exit 1, and skipping it entirely for the claim-exempt
				// branch would silently accept a usage error that every
				// other command (direct or proxied) rejects.
				if _, _, err := resolveMaxRows(cmd); err != nil {
					return err
				}
			}
			return runReadyProxiedServer(cmd, rootCtx)
		}

		if offset, _ := cmd.Flags().GetInt("offset"); offset > 0 {
			return HandleErrorRespectJSON("--offset is only supported under --proxied-server")
		}

		gated, _ := cmd.Flags().GetBool("gated")
		if gated {
			if claimReady {
				return HandleErrorRespectJSON("--claim cannot be combined with --gated")
			}
			// Delegate to the non-emitting core so `bd ready --gated` records
			// exactly one cli_command event ("ready"), not also "mol-ready-gated".
			return runMolReadyGatedCore(cmd, args)
		}

		molID, _ := cmd.Flags().GetString("mol")
		if molID != "" {
			if claimReady {
				return HandleErrorRespectJSON("--claim cannot be combined with --mol")
			}
			return runMoleculeReady(cmd, molID)
		}

		explain, _ := cmd.Flags().GetBool("explain")
		if explain {
			if claimReady {
				return HandleErrorRespectJSON("--claim cannot be combined with --explain")
			}
			return runReadyExplain(cmd)
		}

		limit, _ := cmd.Flags().GetInt("limit")
		assignee, _ := cmd.Flags().GetString("assignee")
		unassigned, _ := cmd.Flags().GetBool("unassigned")
		sortPolicy, _ := cmd.Flags().GetString("sort")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		excludeLabels, _ := cmd.Flags().GetStringSlice("exclude-label")
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
				return HandleErrorRespectJSON("invalid mol-type %q (must be swarm, patrol, or work)", molTypeStr)
			}
			molType = &mt
		}
		if claimReady && assignee != "" {
			return HandleErrorRespectJSON("--claim cannot be combined with --assignee")
		}

		// Normalize labels: trim, dedupe, remove empty
		labels = utils.NormalizeLabels(labels)
		labelsAny = utils.NormalizeLabels(labelsAny)
		excludeLabels = utils.NormalizeLabels(excludeLabels)

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
		maxRows, maxRowsSource, err := resolveMaxRows(cmd)
		if err != nil {
			return err
		}
		filter := types.WorkFilter{
			Status:           "open", // Only show open issues, not in_progress (matches bd list --ready)
			Type:             issueType,
			Limit:            limit,
			Unassigned:       unassigned,
			SortPolicy:       types.SortPolicy(sortPolicy),
			Labels:           labels,
			LabelsAny:        labelsAny,
			ExcludeLabels:    excludeLabels,
			IncludeDeferred:  includeDeferred,  // GH#820: respect --include-deferred flag
			IncludeEphemeral: includeEphemeral, // bd-i5k5x: allow ephemeral issues (e.g., merge-requests)
			ExcludeTypes:     excludeTypes,
			MaxRows:          maxRows,
			MaxRowsSource:    maxRowsSource,
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
					return HandleErrorRespectJSON("invalid --metadata-field: expected key=value, got %q", mf)
				}
				if err := storage.ValidateMetadataKey(k); err != nil {
					return HandleErrorRespectJSON("invalid --metadata-field key: %v", err)
				}
				filter.MetadataFields[k] = v
			}
		}
		hasMetadataKey, _ := cmd.Flags().GetString("has-metadata-key")
		if hasMetadataKey != "" {
			if err := storage.ValidateMetadataKey(hasMetadataKey); err != nil {
				return HandleErrorRespectJSON("invalid --has-metadata-key: %v", err)
			}
			filter.HasMetadataKey = hasMetadataKey
		}

		if !filter.SortPolicy.IsValid() {
			return HandleErrorRespectJSON("invalid sort policy '%s'. Valid values: hybrid, priority, oldest", sortPolicy)
		}
		ctx := rootCtx

		activeStore := store
		if claimReady {
			CheckReadonly("ready --claim")
		} else {
			routedStore, routed, err := openRoutedReadStore(ctx, activeStore)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			if routed {
				defer func() { _ = routedStore.Close() }()
				activeStore = routedStore
			}
		}

		if claimReady {
			claimed, err := activeStore.ClaimReadyIssue(ctx, filter, actor)
			if err != nil {
				if capErr := handleMaxRowsError(err); capErr != nil {
					return capErr
				}
				return HandleErrorRespectJSON("%v", err)
			}
			if claimed == nil {
				if jsonOutput {
					return outputJSON([]*types.IssueWithCounts{})
				}
				fmt.Printf("\n%s No ready work to claim\n\n", ui.RenderWarn("○"))
				return nil
			}
			if err := commitPendingIfEmbedded(ctx, activeStore, actor, doltAutoCommitParams{
				Command:  "ready",
				IssueIDs: []string{claimed.ID},
			}); err != nil {
				return HandleErrorRespectJSON("failed to commit: %v", err)
			}
			SetLastTouchedID(claimed.ID)
			if jsonOutput {
				return outputJSON(buildReadyIssueOutput(ctx, activeStore, []*types.Issue{claimed}))
			}
			fmt.Printf("%s Claimed issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(claimed.ID, claimed.Title))
			return nil
		}

		if jsonOutput {
			results, err := activeStore.GetReadyWorkWithCounts(ctx, filter)
			if err != nil {
				if capErr := handleMaxRowsError(err); capErr != nil {
					return capErr
				}
				return HandleErrorRespectJSON("%v", err)
			}
			totalReady := len(results)
			truncated := false
			if filter.Limit > 0 && len(results) == filter.Limit {
				// The page is full, so there may be more ready work. Size the true
				// total N over the same ready predicate, zeroing the limit so the
				// count is the full ready set (byte-identical to
				// len(GetReadyWorkWithCounts(Limit=0))). Prefer the cheap COUNT(*)
				// capability; unwrap past decorators (e.g. HookFiringStore) so the
				// assertion reaches the concrete store. Fall back to the unbounded
				// mega-query only when a store predates ReadyWorkCounter.
				countFilter := filter
				countFilter.Limit = 0
				if counter, ok := storage.UnwrapStore(activeStore).(storage.ReadyWorkCounter); ok {
					if n, countErr := counter.CountReadyWork(ctx, countFilter); countErr == nil && n > len(results) {
						totalReady = n
						truncated = true
					}
				} else {
					all, countErr := activeStore.GetReadyWorkWithCounts(ctx, countFilter)
					if countErr == nil && len(all) > len(results) {
						totalReady = len(all)
						truncated = true
					}
				}
			}
			if results == nil {
				results = []*types.IssueWithCounts{}
			}
			if jerr := outputJSON(results); jerr != nil {
				return jerr
			}
			if truncated {
				fmt.Fprintf(os.Stderr, "Showing %d of %d ready issues. Use --limit 0 for all, or --limit N to raise the cap.\n", len(results), totalReady)
			}
			return nil
		}

		issues, err := activeStore.GetReadyWork(ctx, filter)
		if err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleErrorRespectJSON("%v", err)
		}

		totalReady := len(issues)
		truncated := false
		if !jsonOutput && filter.Limit > 0 && len(issues) == filter.Limit {
			countFilter := filter
			countFilter.Limit = 0
			allIssues, countErr := activeStore.GetReadyWork(ctx, countFilter)
			if countErr == nil && len(allIssues) > len(issues) {
				totalReady = len(allIssues)
				truncated = true
			}
		}
		maybeShowUpgradeNotification()

		if len(issues) == 0 {
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
			maybeShowTip(store)
			return nil
		}
		parentEpicMap := buildParentEpicMap(ctx, activeStore, issues)

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

		if truncated {
			fmt.Printf("%s\n\n", ui.RenderMuted(fmt.Sprintf("Showing %d of %d ready issues. Use -n to show more.", len(issues), totalReady)))
		}

		maybeShowTip(store)
		return nil
	},
}
var blockedCmd = &cobra.Command{
	Use:           "blocked",
	Short:         "Show blocked issues",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("blocked")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runBlockedProxiedServer(cmd, rootCtx)
		}
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
			return HandleErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			if blocked == nil {
				blocked = []*types.BlockedIssue{}
			}
			return outputJSON(blocked)
		}
		if len(blocked) == 0 {
			fmt.Printf("\n%s No blocked issues\n\n", ui.RenderPass("✨"))
			return nil
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
		return nil
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

func buildReadyIssueOutput(ctx context.Context, s storage.DoltStorage, issues []*types.Issue) []*types.IssueWithCounts {
	if issues == nil {
		issues = []*types.Issue{}
	}
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}

	depCounts, _ := s.GetDependencyCounts(ctx, issueIDs)
	allDeps, _ := s.GetDependencyRecordsForIssues(ctx, issueIDs)
	commentCounts, _ := s.GetCommentCounts(ctx, issueIDs)

	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
	for i, issue := range issues {
		counts := depCounts[issue.ID]
		if counts == nil {
			counts = &types.DependencyCounts{DependencyCount: 0, DependentCount: 0}
		}
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
	return issuesWithCounts
}

func runReadyExplain(_ *cobra.Command) error {
	ctx := rootCtx

	activeStore := store

	filter := types.WorkFilter{
		Status:     types.StatusOpen,
		SortPolicy: types.SortPolicyPriority,
	}
	readyIssues, err := activeStore.GetReadyWork(ctx, filter)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	blockedIssues, err := activeStore.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// Get dependency records for ready issues to find resolved blockers
	readyIDs := make([]string, len(readyIssues))
	for i, issue := range readyIssues {
		readyIDs[i] = issue.ID
	}
	depCounts, err := activeStore.GetDependencyCounts(ctx, readyIDs)
	if err != nil {
		debug.Logf("warning: failed to get dependency counts: %v", err)
	}
	allDeps, err := activeStore.GetDependencyRecordsForIssues(ctx, readyIDs)
	if err != nil {
		debug.Logf("warning: failed to get dependency records: %v", err)
	}

	// Detect cycles
	cycles, err := activeStore.DetectCycles(ctx)
	if err != nil {
		debug.Logf("warning: failed to detect cycles: %v", err)
	}

	// Collect all blocker IDs to batch-fetch blocker details
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

	// Build ready items with explanations
	blockerIssues, err := activeStore.GetIssuesByIDs(ctx, blockerIDList)
	if err != nil {
		debug.Logf("warning: failed to get blocker issues: %v", err)
	}
	blockerMap := make(map[string]*types.Issue, len(blockerIssues))
	for _, issue := range blockerIssues {
		blockerMap[issue.ID] = issue
	}

	explanation := types.BuildReadyExplanation(readyIssues, blockedIssues, depCounts, allDeps, blockerMap, cycles)

	if jsonOutput {
		return outputJSON(explanation)
	}

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
	return nil
}

func runMoleculeReady(_ *cobra.Command, molIDArg string) error {
	ctx := rootCtx

	if store == nil {
		return HandleErrorRespectJSON("no database connection")
	}

	moleculeID, err := utils.ResolvePartialID(ctx, store, molIDArg)
	if err != nil {
		return HandleErrorRespectJSON("molecule '%s' not found", molIDArg)
	}

	subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
	if err != nil {
		return HandleErrorRespectJSON("loading molecule: %v", err)
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
		return outputJSON(MoleculeReadyOutput{
			MoleculeID:     moleculeID,
			MoleculeTitle:  subgraph.Root.Title,
			TotalSteps:     analysis.TotalSteps,
			ReadySteps:     len(readySteps),
			Steps:          readySteps,
			ParallelGroups: analysis.ParallelGroups,
		})
	}

	fmt.Printf("\n%s Ready steps in molecule: %s\n", ui.RenderAccent("🧪"), subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", moleculeID)
	fmt.Printf("   Total: %d steps, %d ready\n", analysis.TotalSteps, len(readySteps))

	if len(readySteps) == 0 {
		fmt.Printf("\n%s No ready steps (all blocked or completed)\n\n", ui.RenderWarn("✨"))
		return nil
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
	return nil
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
	readyCmd.Flags().IntP("limit", "n", 100, "Maximum issues to show (use 0 for unlimited)")
	readyCmd.Flags().Int("offset", 0, "Skip the first N matching results (0-based). Only supported under --proxied-server.")
	readyCmd.Flags().IntP("priority", "p", 0, "Filter by priority")
	readyCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	readyCmd.Flags().BoolP("unassigned", "u", false, "Show only unassigned issues")
	readyCmd.Flags().StringP("sort", "s", "priority", "Sort policy: priority (default), hybrid, oldest")
	readyCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	readyCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	readyCmd.Flags().StringSlice("exclude-label", []string{}, "Exclude issues that have ANY of these labels")
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
	readyCmd.Flags().Bool("explain", false, "Show dependency-aware reasoning for why issues are ready or blocked")
	readyCmd.Flags().Bool("claim", false, "Atomically claim the first ready issue matching the filters")
	// Metadata filtering (GH#1406)
	readyCmd.Flags().StringArray("metadata-field", nil, "Filter by metadata field (key=value, repeatable)")
	readyCmd.Flags().String("has-metadata-key", "", "Filter issues that have this metadata key set")
	// Defensive row cap (be-x42v): exits 2 on overage, default disabled.
	addMaxRowsFlag(readyCmd)
	rootCmd.AddCommand(readyCmd)
	blockedCmd.Flags().String("parent", "", "Filter to descendants of this bead/epic")
	rootCmd.AddCommand(blockedCmd)
}
