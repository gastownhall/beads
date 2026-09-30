package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
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

	// No cap resolver: --max-rows / BEADS_MAX_ROWS has already been resolved
	// twice before this route runs — once at the proxied front door and once
	// in the RunE that routed here — and any positive cap was refused by both,
	// on --claim as much as on a bulk read. Resolving it a third time would
	// repeat the malformed-value warning and stamp a cap this route cannot
	// enforce.
	in, err := gatherReadyInput(cmd, nil)
	if err != nil {
		return err
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	if in.claim {
		return runReadyProxiedClaim(ctx, in)
	}
	if !in.gated && in.molID == "" && !in.explain {
		return runReadyProxiedList(ctx, in)
	}

	// Wake expired dated defers before the read below. The unit of work this
	// route opens is read-only-by-ending (Close rolls back), so the sweep runs
	// in a committing UOW of its own first; the claim and listing routes above
	// get the same sweep from their roles (uow.readyClaimer.ClaimNext,
	// uow.readyLister.ListReady).
	uow.WakeExpiredDefersAdvisory(ctx, uowProvider)

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
	default:
		return runReadyProxiedExplain(ctx, uw, in)
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

	filter := blockedFilterFromFlags(cmd)

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

// runReadyProxiedList is the proxied route of `bd ready`'s listing, on the
// same ReadyLister role the direct route reaches through the store's accessor,
// with the same request (readyInput.ReadyListRequest): the page and the size of
// the whole ready set come back from ONE call, inside one read-only unit of
// work the role opens itself, and the external-dependency policy the provider
// carries is the role's layer rather than this function's. This function
// builds no filter and opens no unit of work for the listing; it opens one only
// to render the text route's extras (the empty-state statistics and the parent
// epic titles), which are presentation, not the answer.
func runReadyProxiedList(ctx context.Context, in readyInput) error {
	lister, err := proxiedReadyLister()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	listing, err := lister.ListReady(ctx, in.ReadyListRequest)
	if err != nil {
		return HandleError("%v", err)
	}
	// The rendering is the direct route's, through the same function: one
	// JSON shape, one pagination envelope, one "Showing X of N" hint. Only the
	// text route's extras are read through this route's own plumbing, in a
	// unit of work opened for them alone — and only if the text route asks.
	var uw uow.UnitOfWork
	openUW := func() uow.UnitOfWork {
		if uw == nil {
			if opened, uwErr := uowProvider.NewUOW(ctx); uwErr == nil {
				uw = opened
			} else {
				debug.Logf("warning: open unit of work for ready rendering: %v", uwErr)
			}
		}
		return uw
	}
	defer func() {
		if uw != nil {
			uw.Close(ctx)
		}
	}()
	return renderReadyListing(listing, in, readyListingExtras{
		hasOpenIssues: func() bool {
			w := openUW()
			if w == nil {
				return false
			}
			stats, statsErr := w.IssueUseCase().GetStatistics(ctx)
			return statsErr == nil && (stats.OpenIssues > 0 || stats.InProgressIssues > 0)
		},
		parentEpics: func(issues []*types.Issue) map[string]string {
			w := openUW()
			if w == nil {
				return nil
			}
			return buildParentEpicMapProxied(ctx, w, issues)
		},
	})
}

// proxiedReadyLister hands back the ready-listing surface for the
// proxied-server provider through the provider's OWN capability accessor — the
// accessor is where a decorator (the external-dependency policy, telemetry)
// adds its layer, so reaching past it would list unpoliced.
func proxiedReadyLister() (issueops.ReadyLister, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.ReadyListerSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the ready-listing surface", uowProvider)
	}
	return src.ReadyLister()
}

// runReadyProxiedClaim is the proxied route of `bd ready --claim`, on the same
// ReadyClaimer role the direct route reaches through the store's accessor.
// Both build their request in claimNextRequest, so the two doors ask one
// question — and neither opens a unit of work of its own: the role's request
// IS the transaction, which is what lets the selection, the compare-and-set
// and the hydration this route prints share it.
func runReadyProxiedClaim(ctx context.Context, in readyInput) error {
	CheckReadonly("ready --claim")

	claimer, err := proxiedReadyClaimer()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	res, err := claimer.ClaimNext(ctx, claimNextRequest(in))
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if res.Claimed == nil {
		if in.jsonOut {
			_ = outputJSON([]*types.IssueWithCounts{})
		} else {
			fmt.Printf("\n%s No ready work to claim\n\n", ui.RenderWarn("○"))
		}
		return nil
	}

	SetLastTouchedID(res.Claimed.ID)

	if in.jsonOut {
		_ = outputJSON([]*types.IssueWithCounts{res.Claimed})
	} else {
		fmt.Printf("%s Claimed issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(res.Claimed.ID, res.Claimed.Title))
	}
	return nil
}

// proxiedReadyClaimer hands back the guarded claim surface for the
// proxied-server provider, through the provider's OWN capability accessor —
// the same two-step proxiedIssueReader performs, and for the same reason: the
// accessor is where a decorator adds its layer.
func proxiedReadyClaimer() (issueops.ReadyClaimer, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.ReadyClaimerSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the ready-claim surface", uowProvider)
	}
	return src.ReadyClaimer()
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
