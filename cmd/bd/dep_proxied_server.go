package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

type depAddResult struct {
	fromTitle string
	toTitle   string
	cycles    [][]*types.Issue
	cycleErr  error
}

func proxiedLookupTitle(ctx context.Context, uw uow.UnitOfWork, id string) string {
	if IsExternalRef(id) {
		return ""
	}
	issue, err := uw.IssueUseCase().GetIssue(ctx, id)
	if err == nil && issue != nil {
		return issue.Title
	}
	wisp, err := uw.IssueUseCase().GetWisp(ctx, id)
	if err == nil && wisp != nil {
		return wisp.Title
	}
	return ""
}

func printCycleDetectionError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to check for cycles: %v\n", err)
	}
}

func printCycleWarnings(cycles [][]*types.Issue) {
	if len(cycles) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s Warning: Dependency cycle detected!\n", ui.RenderWarn("⚠"))
	fmt.Fprintf(os.Stderr, "This can hide issues from the ready work list and cause confusion.\n\n")
	fmt.Fprintf(os.Stderr, "Cycle path:\n")
	for _, cycle := range cycles {
		for j, issue := range cycle {
			if j == 0 {
				fmt.Fprintf(os.Stderr, "  %s", issue.ID)
			} else {
				fmt.Fprintf(os.Stderr, " → %s", issue.ID)
			}
		}
		if len(cycle) > 0 {
			fmt.Fprintf(os.Stderr, " → %s", cycle[0].ID)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}
	fmt.Fprintf(os.Stderr, "\nRun 'bd dep cycles' for detailed analysis.\n\n")
}

func runDepBlocksProxiedServer(cmd *cobra.Command, ctx context.Context, blockerID, blockedID string) error {
	if isDisallowedHierarchicalDependency(blockedID, blockerID, types.DepBlocks) {
		return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", blockedID, blockerID)
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	noCycleCheck, _ := cmd.Flags().GetBool("no-cycle-check")

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (depAddResult, string, error) {
		dep := &types.Dependency{
			IssueID:     blockedID,
			DependsOnID: blockerID,
			Type:        types.DepBlocks,
		}
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actor, domain.BulkAddDepsOpts{}); err != nil {
			return depAddResult{}, "", err
		}

		var cycles [][]*types.Issue
		var cycleErr error
		if !noCycleCheck {
			cycles, cycleErr = uw.DependencyUseCase().DetectCycles(ctx)
		}

		return depAddResult{
			fromTitle: proxiedLookupTitle(ctx, uw, blockedID),
			toTitle:   proxiedLookupTitle(ctx, uw, blockerID),
			cycles:    cycles,
			cycleErr:  cycleErr,
		}, fmt.Sprintf("bd: dep add %s %s", blockedID, blockerID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	printCycleDetectionError(res.cycleErr)
	printCycleWarnings(res.cycles)

	if jsonOutput {
		_ = outputJSON(map[string]interface{}{
			"status":     "added",
			"blocker_id": blockerID,
			"blocked_id": blockedID,
			"type":       string(types.DepBlocks),
		})
		return nil
	}

	fmt.Printf("%s Added dependency: %s blocks %s\n",
		ui.RenderPass("✓"),
		formatFeedbackIDParen(blockerID, res.toTitle),
		formatFeedbackIDParen(blockedID, res.fromTitle))
	return nil
}

func runDepAddProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	depType, _ := cmd.Flags().GetString("type")
	file, _ := cmd.Flags().GetString("file")

	if file != "" {
		return runDepAddBulkProxied(cmd, ctx, file, depType)
	}

	blockedBy, _ := cmd.Flags().GetString("blocked-by")
	dependsOn, _ := cmd.Flags().GetString("depends-on")

	var dependsOnArg string
	switch {
	case blockedBy != "":
		dependsOnArg = blockedBy
	case dependsOn != "":
		dependsOnArg = dependsOn
	default:
		dependsOnArg = args[1]
	}

	fromID := args[0]
	var toID string
	if strings.HasPrefix(dependsOnArg, "external:") {
		if err := validateExternalRef(dependsOnArg); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		toID = dependsOnArg
	} else {
		toID = dependsOnArg
	}

	dt := types.DependencyType(depType)
	if isDisallowedHierarchicalDependency(fromID, toID, dt) {
		return HandleErrorRespectJSON("cannot add dependency: %s is already a child of %s. Children inherit dependency on parent completion via hierarchy. Adding an explicit dependency would create a deadlock", fromID, toID)
	}

	if !dt.IsValid() {
		return HandleErrorRespectJSON("invalid dependency type %q: must be non-empty and at most 50 characters", depType)
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	noCycleCheck, _ := cmd.Flags().GetBool("no-cycle-check")

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (depAddResult, string, error) {
		dep := &types.Dependency{IssueID: fromID, DependsOnID: toID, Type: dt}
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actor, domain.BulkAddDepsOpts{}); err != nil {
			return depAddResult{}, "", err
		}

		var cycles [][]*types.Issue
		var cycleErr error
		if !noCycleCheck {
			cycles, cycleErr = uw.DependencyUseCase().DetectCycles(ctx)
		}

		return depAddResult{
			fromTitle: proxiedLookupTitle(ctx, uw, fromID),
			toTitle:   proxiedLookupTitle(ctx, uw, toID),
			cycles:    cycles,
			cycleErr:  cycleErr,
		}, fmt.Sprintf("bd: dep add %s %s", fromID, toID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	printCycleDetectionError(res.cycleErr)
	printCycleWarnings(res.cycles)

	if jsonOutput {
		_ = outputJSON(map[string]interface{}{
			"status":        "added",
			"issue_id":      fromID,
			"depends_on_id": toID,
			"type":          depType,
		})
		return nil
	}

	fmt.Printf("%s Added dependency: %s depends on %s (%s)\n",
		ui.RenderPass("✓"),
		formatFeedbackIDParen(fromID, res.fromTitle),
		formatFeedbackIDParen(toID, res.toTitle),
		depType)
	return nil
}

func runDepAddBulkProxied(cmd *cobra.Command, ctx context.Context, file, defaultType string) error {
	edges, err := readBulkDepEdges(file, defaultType)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if len(edges) == 0 {
		return HandleErrorRespectJSON("no dependency edges found")
	}

	deps := make([]*types.Dependency, 0, len(edges))
	for _, edge := range edges {
		if isDisallowedHierarchicalDependency(edge.IssueID, edge.DependsOnID, edge.Type) {
			return HandleErrorRespectJSON("line %d: cannot add dependency: %s is already a child of %s", edge.Line, edge.IssueID, edge.DependsOnID)
		}
		if strings.HasPrefix(edge.DependsOnID, "external:") {
			if err := validateExternalRef(edge.DependsOnID); err != nil {
				return HandleErrorRespectJSON("line %d: %v", edge.Line, err)
			}
		}
		deps = append(deps, &types.Dependency{
			IssueID:     edge.IssueID,
			DependsOnID: edge.DependsOnID,
			Type:        edge.Type,
		})
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	noCycleCheck, _ := cmd.Flags().GetBool("no-cycle-check")

	type bulkResult struct {
		cycles   [][]*types.Issue
		cycleErr error
	}
	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (bulkResult, string, error) {
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, deps, actor, domain.BulkAddDepsOpts{
			SkipPerEdgeCycleCheck: noCycleCheck,
		}); err != nil {
			return bulkResult{}, "", err
		}

		var r bulkResult
		if !noCycleCheck {
			r.cycles, r.cycleErr = uw.DependencyUseCase().DetectCycles(ctx)
		}

		return r, fmt.Sprintf("dependency: add %d edges", len(deps)), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	printCycleDetectionError(res.cycleErr)
	printCycleWarnings(res.cycles)

	if jsonOutput {
		out := make([]map[string]interface{}, 0, len(deps))
		for _, dep := range deps {
			out = append(out, map[string]interface{}{
				"issue_id":      dep.IssueID,
				"depends_on_id": dep.DependsOnID,
				"type":          string(dep.Type),
			})
		}
		_ = outputJSON(map[string]interface{}{
			"status":       "added",
			"count":        len(deps),
			"dependencies": out,
		})
		return nil
	}

	fmt.Printf("%s Added %d dependencies\n", ui.RenderPass("✓"), len(deps))
	return nil
}

func runDepRemoveProxiedServer(_ *cobra.Command, ctx context.Context, args []string) error {
	fromID := args[0]
	toID := args[1]
	if strings.HasPrefix(toID, "external:") {
		if err := validateExternalRef(toID); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (depAddResult, string, error) {
		if err := uw.DependencyUseCase().RemoveDependency(ctx, fromID, toID, actor); err != nil {
			return depAddResult{}, "", err
		}
		return depAddResult{
			fromTitle: proxiedLookupTitle(ctx, uw, fromID),
			toTitle:   proxiedLookupTitle(ctx, uw, toID),
		}, fmt.Sprintf("bd: dep remove %s %s", fromID, toID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		_ = outputJSON(map[string]interface{}{
			"status":        "removed",
			"issue_id":      fromID,
			"depends_on_id": toID,
		})
		return nil
	}

	fmt.Printf("%s Removed dependency: %s no longer depends on %s\n",
		ui.RenderPass("✓"),
		formatFeedbackIDParen(fromID, res.fromTitle),
		formatFeedbackIDParen(toID, res.toTitle))
	return nil
}

func runDepListProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	direction, _ := cmd.Flags().GetString("direction")
	typeFilter, _ := cmd.Flags().GetString("type")
	if direction == "" {
		direction = "down"
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	depUC := uw.DependencyUseCase()

	if len(args) > 1 && direction == "down" {
		depMap, err := depUC.GetIssueDependencyRecords(ctx, args)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		var allDeps []*types.Dependency
		for _, id := range args {
			for _, dep := range depMap[id] {
				if typeFilter == "" || string(dep.Type) == typeFilter {
					allDeps = append(allDeps, dep)
				}
			}
		}
		if jsonOutput {
			if allDeps == nil {
				allDeps = []*types.Dependency{}
			}
			_ = outputJSON(allDeps)
			return nil
		}
		for _, id := range args {
			deps := depMap[id]
			if len(deps) == 0 {
				fmt.Printf("\n%s has no dependencies\n", id)
				continue
			}
			fmt.Printf("\n%s %s depends on:\n\n", ui.RenderAccent("📋"), id)
			for _, dep := range deps {
				if typeFilter != "" && string(dep.Type) != typeFilter {
					continue
				}
				fmt.Printf("  %s via %s\n", dep.DependsOnID, dep.Type)
			}
		}
		fmt.Println()
		return nil
	}

	var allIssues []*types.IssueWithDependencyMetadata
	listDirection := domain.DepDirectionOut
	if direction == "up" {
		listDirection = domain.DepDirectionIn
	}
	for _, id := range args {
		issues, err := depUC.ListWithIssueMetadata(ctx, id, domain.DepListFilter{Direction: listDirection})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if typeFilter != "" {
			filtered := issues[:0]
			for _, iss := range issues {
				if string(iss.DependencyType) == typeFilter {
					filtered = append(filtered, iss)
				}
			}
			issues = filtered
		}
		allIssues = append(allIssues, issues...)
	}

	if jsonOutput {
		if allIssues == nil {
			allIssues = []*types.IssueWithDependencyMetadata{}
		}
		_ = outputJSON(allIssues)
		return nil
	}

	if len(allIssues) == 0 {
		if len(args) == 1 {
			if direction == "up" {
				fmt.Printf("\nNo issues depend on %s\n", args[0])
			} else {
				fmt.Printf("\n%s has no dependencies\n", args[0])
			}
		} else {
			fmt.Println("\nNo dependencies found")
		}
		return nil
	}

	for _, iss := range allIssues {
		var idStr string
		switch iss.Status {
		case types.StatusOpen:
			idStr = ui.StatusOpenStyle.Render(iss.ID)
		case types.StatusInProgress:
			idStr = ui.StatusInProgressStyle.Render(iss.ID)
		case types.StatusBlocked:
			idStr = ui.StatusBlockedStyle.Render(iss.ID)
		case types.StatusClosed:
			idStr = ui.StatusClosedStyle.Render(iss.ID)
		default:
			idStr = iss.ID
		}
		fmt.Printf("  %s: %s [P%d] (%s) via %s\n",
			idStr, iss.Title, iss.Priority, iss.Status, iss.DependencyType)
	}
	fmt.Println()
	return nil
}

func runDepTreeProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	fullID := args[0]
	showAllPaths, _ := cmd.Flags().GetBool("show-all-paths")
	maxDepth, _ := cmd.Flags().GetInt("max-depth")
	reverse, _ := cmd.Flags().GetBool("reverse")
	direction, _ := cmd.Flags().GetString("direction")
	statusFilter, _ := cmd.Flags().GetString("status")
	formatStr, _ := cmd.Flags().GetString("format")
	if strings.EqualFold(formatStr, "json") {
		jsonOutput = true
		formatStr = ""
	}
	if direction == "" && reverse {
		direction = "up"
	} else if direction == "" {
		direction = "down"
	}
	if direction != "down" && direction != "up" && direction != "both" {
		return HandleErrorRespectJSON("--direction must be 'down', 'up', or 'both'")
	}
	if maxDepth < 1 {
		return HandleErrorRespectJSON("--max-depth must be >= 1")
	}

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	depUC := uw.DependencyUseCase()
	var tree []*types.TreeNode

	if direction == "both" {
		downTree, err := depUC.GetDependencyTree(ctx, fullID, domain.DepTreeOpts{
			MaxDepth:     maxDepth,
			ShowAllPaths: showAllPaths,
			Direction:    domain.DepDirectionOut,
		})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		upTree, err := depUC.GetDependencyTree(ctx, fullID, domain.DepTreeOpts{
			MaxDepth:     maxDepth,
			ShowAllPaths: showAllPaths,
			Direction:    domain.DepDirectionIn,
		})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		tree = mergeBidirectionalTrees(downTree, upTree, fullID)
	} else {
		treeDir := domain.DepDirectionOut
		if direction == "up" {
			treeDir = domain.DepDirectionIn
		}
		var err error
		tree, err = depUC.GetDependencyTree(ctx, fullID, domain.DepTreeOpts{
			MaxDepth:     maxDepth,
			ShowAllPaths: showAllPaths,
			Direction:    treeDir,
		})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
	}

	if statusFilter != "" {
		tree = filterTreeByStatus(tree, types.Status(statusFilter))
	}

	if formatStr == "mermaid" {
		outputMermaidTree(tree, args[0])
		return nil
	}

	if jsonOutput {
		if tree == nil {
			tree = []*types.TreeNode{}
		}
		_ = outputJSON(tree)
		return nil
	}

	if len(tree) == 0 {
		switch direction {
		case "up":
			fmt.Printf("\n%s has no dependents\n", fullID)
		case "both":
			fmt.Printf("\n%s has no dependencies or dependents\n", fullID)
		default:
			fmt.Printf("\n%s has no dependencies\n", fullID)
		}
		return nil
	}

	switch direction {
	case "up":
		fmt.Printf("\n%s Dependent tree for %s:\n\n", ui.RenderAccent("🌲"), fullID)
	case "both":
		fmt.Printf("\n%s Full dependency graph for %s:\n\n", ui.RenderAccent("🌲"), fullID)
	default:
		fmt.Printf("\n%s Dependency tree for %s:\n\n", ui.RenderAccent("🌲"), fullID)
	}

	renderTree(tree, maxDepth, direction)
	fmt.Println()
	return nil
}

func runDepCyclesProxiedServer(_ *cobra.Command, ctx context.Context) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	cycles, err := uw.DependencyUseCase().DetectCycles(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		if cycles == nil {
			cycles = [][]*types.Issue{}
		}
		_ = outputJSON(cycles)
		return nil
	}

	if len(cycles) == 0 {
		fmt.Printf("\n%s No dependency cycles detected\n\n", ui.RenderPass("✓"))
		return nil
	}

	fmt.Printf("\n%s Found %d dependency cycles:\n\n", ui.RenderFail("⚠"), len(cycles))
	for i, cycle := range cycles {
		fmt.Printf("%d. Cycle involving:\n", i+1)
		for _, issue := range cycle {
			fmt.Printf("   - %s: %s\n", issue.ID, issue.Title)
		}
		fmt.Println()
	}
	return nil
}
