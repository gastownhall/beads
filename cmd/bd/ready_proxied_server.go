package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

func runReadyProxiedServer(cmd *cobra.Command, ctx context.Context) error {
	// --offset is supported here and nowhere else, so this is where a negative
	// value is rejected. It stays out of the shared gatherer because the direct
	// route reaches that gatherer too, and `bd ready --offset -1` has always
	// been a no-op there rather than an error. HandleError, not the RespectJSON
	// variant, for the same reason: this message is the proxied route's alone,
	// and it has always gone to stderr as plain text.
	if offset, _ := cmd.Flags().GetInt("offset"); offset < 0 {
		return HandleError("--offset must be >= 0")
	}

	// No cap resolver: the RunE that routed here has already resolved
	// --max-rows / BEADS_MAX_ROWS, either to reject a live one or to validate
	// the value it then ignores for --claim. Resolving it again would repeat
	// the malformed-value warning and stamp a cap this route cannot enforce.
	in, err := gatherReadyInput(cmd, nil)
	if err != nil {
		return err
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	if in.claim {
		return runReadyProxiedClaim(ctx, nil, in)
	}

	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	switch {
	case in.gated:
		return runReadyProxiedGated(ctx, uw, in)
	case in.molID != "":
		return runReadyProxiedMolecule(ctx, uw, in)
	case in.explain:
		return runReadyProxiedExplain(ctx, uw, in)
	default:
		return runReadyProxiedList(ctx, uw, in)
	}
}

func runBlockedProxiedServer(cmd *cobra.Command, ctx context.Context) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	var filter types.WorkFilter
	if parentID, _ := cmd.Flags().GetString("parent"); parentID != "" {
		filter.ParentID = &parentID
	}

	blocked, err := uw.IssueUseCase().GetBlockedIssues(ctx, filter)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		if blocked == nil {
			blocked = []*types.BlockedIssue{}
		}
		_ = outputJSON(blocked)
		return nil
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
}

func runReadyProxiedList(ctx context.Context, uw uow.UnitOfWork, in readyInput) error {
	if in.jsonOut {
		page, err := uw.IssueUseCase().GetReadyWorkWithCounts(ctx, in.filter)
		if err != nil {
			return HandleError("%v", err)
		}
		// The same epilogue issueops.Reader.Ready runs, through the same
		// function: this seam reports a has-more natively and ready has no
		// display order, so the trim is a no-op and the verdict is the seam's
		// — but it is reached the one way, not restated here.
		results, truncated := workapi.FinishPage(page.Items, "", false, in.filter.Limit, page.HasMore)
		truncated = truncated && in.filter.Limit > 0
		// Parity with the direct route: the pagination key is emitted only
		// when truncated. Total is unavailable from this backend's domain
		// page (no cheap COUNT(*) equivalent on the proxied path), so it is
		// left unset (0) unlike the direct route's fully-populated meta.
		var pag *PaginationMeta
		if truncated {
			pag = &PaginationMeta{
				Returned:  len(results),
				Truncated: true,
			}
		}
		_ = outputJSONWithPagination(results, pag)
		if truncated {
			fmt.Fprintf(os.Stderr, "Showing %d ready issues; more matched but were hidden by --limit. Use --limit 0 for all, or --limit N to raise the cap.\n", len(results))
		}
		return nil
	}

	page, err := uw.IssueUseCase().GetReadyWork(ctx, in.filter)
	if err != nil {
		return HandleError("%v", err)
	}
	issues, truncated := workapi.FinishPage(page.Items, "", false, in.filter.Limit, page.HasMore)
	truncated = truncated && in.filter.Limit > 0

	maybeShowUpgradeNotification()

	if len(issues) == 0 {
		hasOpenIssues := false
		if stats, statsErr := uw.IssueUseCase().GetStatistics(ctx); statsErr == nil {
			hasOpenIssues = stats.OpenIssues > 0 || stats.InProgressIssues > 0
		}
		if hasOpenIssues {
			fmt.Printf("\n%s No ready work found (all issues have blocking dependencies)\n\n",
				ui.RenderWarn("✨"))
		} else {
			fmt.Printf("\n%s No open issues\n\n", ui.RenderPass("✨"))
		}
		return nil
	}

	parentEpicMap := buildParentEpicMapProxied(ctx, uw, issues)
	usePlain := in.plainFormat || !in.prettyFormat
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
		fmt.Printf("%s\n\n", ui.RenderMuted(fmt.Sprintf("Showing %d ready issues; more matched but were hidden by --limit. Use --limit 0 for all, or --limit N to raise the cap.", len(issues))))
	}
	return nil
}

func runReadyProxiedClaim(ctx context.Context, _ uow.UnitOfWork, in readyInput) error {
	CheckReadonly("ready --claim")

	type claimResult struct {
		issue       *types.Issue
		claimed     bool
		jsonPayload []*types.IssueWithCounts
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (claimResult, string, error) {
		claimRes, err := uw.IssueUseCase().ClaimReadyIssue(ctx, in.filter, actor)
		if err != nil {
			return claimResult{}, "", err
		}
		if !claimRes.Claimed {
			return claimResult{claimed: false}, "", nil
		}

		var jsonPayload []*types.IssueWithCounts
		if in.jsonOut {
			jsonPayload = buildReadyIssueOutputProxied(ctx, uw, []*types.Issue{claimRes.Issue})
		}

		return claimResult{
			issue:       claimRes.Issue,
			claimed:     true,
			jsonPayload: jsonPayload,
		}, fmt.Sprintf("bd: ready --claim %s", claimRes.Issue.ID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if !res.claimed {
		if in.jsonOut {
			_ = outputJSON([]*types.IssueWithCounts{})
		} else {
			fmt.Printf("\n%s No ready work to claim\n\n", ui.RenderWarn("○"))
		}
		return nil
	}

	SetLastTouchedID(res.issue.ID)

	if in.jsonOut {
		_ = outputJSON(res.jsonPayload)
	} else {
		fmt.Printf("%s Claimed issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(res.issue.ID, res.issue.Title))
	}
	return nil
}

func runReadyProxiedExplain(ctx context.Context, uw uow.UnitOfWork, _ readyInput) error {
	filter, err := readyExplainFilter()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	readyPage, err := uw.IssueUseCase().GetReadyWork(ctx, filter)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	readyIssues := readyPage.Items

	blockedIssues, err := uw.IssueUseCase().GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	readyIDs := make([]string, len(readyIssues))
	for i, issue := range readyIssues {
		readyIDs[i] = issue.ID
	}
	depCountsMap, err := uw.DependencyUseCase().CountsByIssueIDs(ctx, readyIDs)
	if err != nil {
		debug.Logf("warning: failed to get dependency counts: %v", err)
	}
	depCounts := make(map[string]*types.DependencyCounts, len(depCountsMap))
	for k, v := range depCountsMap {
		depCounts[k] = v
	}
	allDeps, err := uw.DependencyUseCase().GetForIssueIDs(ctx, readyIDs)
	if err != nil {
		debug.Logf("warning: failed to get dependency records: %v", err)
	}

	cycles, err := uw.DependencyUseCase().DetectCycles(ctx)
	if err != nil {
		debug.Logf("warning: failed to detect cycles: %v", err)
	}

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
	blockerIssues, err := uw.IssueUseCase().GetIssuesByIDs(ctx, blockerIDList)
	if err != nil {
		debug.Logf("warning: failed to get blocker issues: %v", err)
	}
	blockerWisps, err := uw.IssueUseCase().GetWispsByIDs(ctx, blockerIDList)
	if err != nil {
		debug.Logf("warning: failed to get blocker wisps: %v", err)
	}
	blockerMap := make(map[string]*types.Issue, len(blockerIssues)+len(blockerWisps))
	for _, issue := range blockerIssues {
		blockerMap[issue.ID] = issue
	}
	for _, wisp := range blockerWisps {
		blockerMap[wisp.ID] = wisp
	}

	explanation := types.BuildReadyExplanation(readyIssues, blockedIssues, depCounts, allDeps, blockerMap, cycles)

	if jsonOutput {
		_ = outputJSON(explanation)
		return nil
	}

	fmt.Printf("\n%s Ready Work Explanation\n\n", ui.RenderAccent("📊"))
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
	if len(explanation.Cycles) > 0 {
		fmt.Printf("%s Cycles detected (%d):\n\n", ui.RenderFail("⚠"), len(explanation.Cycles))
		for _, cycle := range explanation.Cycles {
			fmt.Printf("  %s → %s\n", strings.Join(cycle, " → "), cycle[0])
		}
		fmt.Println()
	}
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

func runReadyProxiedMolecule(ctx context.Context, uw uow.UnitOfWork, in readyInput) error {
	moleculeID := in.molID
	subgraph, err := loadTemplateSubgraph(ctx, uowMolReader{uw: uw}, moleculeID)
	if err != nil {
		return HandleError("loading molecule: %v", err)
	}

	analysis := analyzeMoleculeParallel(subgraph)

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

	if in.jsonOut {
		output := MoleculeReadyOutput{
			MoleculeID:     moleculeID,
			MoleculeTitle:  subgraph.Root.Title,
			TotalSteps:     analysis.TotalSteps,
			ReadySteps:     len(readySteps),
			Steps:          readySteps,
			ParallelGroups: analysis.ParallelGroups,
		}
		_ = outputJSON(output)
		return nil
	}

	fmt.Printf("\n%s Ready steps in molecule: %s\n", ui.RenderAccent("🧪"), subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", moleculeID)
	fmt.Printf("   Total: %d steps, %d ready\n", analysis.TotalSteps, len(readySteps))
	if len(readySteps) == 0 {
		fmt.Printf("\n%s No ready steps (all blocked or completed)\n\n", ui.RenderWarn("✨"))
		return nil
	}
	if len(analysis.ParallelGroups) > 0 {
		fmt.Printf("\n%s Parallel Groups:\n", ui.RenderPass("⚡"))
		for groupName, members := range analysis.ParallelGroups {
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

func runReadyProxiedGated(ctx context.Context, uw uow.UnitOfWork, _ readyInput) error {
	molecules, err := findGateReadyMolecules(ctx, uowMolReader{uw: uw})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	return renderGatedReadyMolecules(molecules)
}

func buildReadyIssueOutputProxied(ctx context.Context, uw uow.UnitOfWork, issues []*types.Issue) []*types.IssueWithCounts {
	if issues == nil {
		issues = []*types.Issue{}
	}
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}

	depCounts, _ := uw.DependencyUseCase().CountsByIssueIDs(ctx, ids)
	allDeps, _ := uw.DependencyUseCase().GetForIssueIDs(ctx, ids)
	commentCounts, _ := uw.CommentUseCase().GetCommentCounts(ctx, ids)

	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	out := make([]*types.IssueWithCounts, len(issues))
	for i, issue := range issues {
		counts := depCounts[issue.ID]
		if counts == nil {
			counts = &types.DependencyCounts{}
		}
		var parent *string
		for _, dep := range allDeps[issue.ID] {
			if dep.Type == types.DepParentChild {
				parent = &dep.DependsOnID
				break
			}
		}
		out[i] = &types.IssueWithCounts{
			Issue:           issue,
			DependencyCount: counts.DependencyCount,
			DependentCount:  counts.DependentCount,
			CommentCount:    commentCounts[issue.ID],
			Parent:          parent,
		}
	}
	return out
}

func buildParentEpicMapProxied(ctx context.Context, uw uow.UnitOfWork, issues []*types.Issue) map[string]string {
	if len(issues) == 0 {
		return nil
	}
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	allDeps, err := uw.DependencyUseCase().GetForIssueIDs(ctx, ids)
	if err != nil {
		return nil
	}
	parentIDs := make(map[string]bool)
	childToParent := make(map[string]string)
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
	epicTitles := make(map[string]string)
	for parentID := range parentIDs {
		parent, err := uw.IssueUseCase().GetIssue(ctx, parentID)
		if err != nil || parent == nil {
			continue
		}
		if parent.IssueType == "epic" {
			epicTitles[parentID] = parent.Title
		}
	}
	result := make(map[string]string)
	for childID, parentID := range childToParent {
		if title, ok := epicTitles[parentID]; ok {
			result[childID] = title
		}
	}
	return result
}
