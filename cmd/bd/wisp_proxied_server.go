package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

func runWispCreateProxiedServer(ctx context.Context, in wispCreateInput) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	vars, err := parseVarFlags(in.varFlags)
	if err != nil {
		return HandleError("%v", err)
	}

	var formulaSubgraph *TemplateSubgraph
	var formulaProtoID string
	if sg, err := resolveAndCookFormulaWithVars(in.protoArg, nil, vars); err == nil {
		formulaSubgraph = sg
		formulaProtoID = sg.Root.ID
	}

	if formulaSubgraph == nil {
		uw, err := proxiedOpenReadUOW(ctx)
		if err != nil {
			return err
		}
		r := uowMolReader{uw: uw}
		protoID, err := utils.ResolvePartialID(ctx, r, in.protoArg)
		if err != nil {
			uw.Close(ctx)
			return HandleErrorWithHint(fmt.Sprintf("'%s' not found as formula or proto", in.protoArg), "run 'bd formula list' to see available formulas")
		}
		protoIssue, err := r.GetIssue(ctx, protoID)
		if err != nil {
			uw.Close(ctx)
			return HandleError("loading proto %s: %v", protoID, err)
		}
		if !isProtoIssue(protoIssue) {
			uw.Close(ctx)
			return HandleError("%s is not a proto (missing '%s' label)", protoID, MoleculeLabel)
		}
		sg, err := loadTemplateSubgraph(ctx, r, protoID)
		uw.Close(ctx)
		if err != nil {
			return HandleError("loading proto: %v", err)
		}
		formulaSubgraph = sg
		formulaProtoID = protoID
	}

	subgraph := formulaSubgraph
	protoID := formulaProtoID
	vars = applyVariableDefaults(vars, subgraph)

	if err := checkRequiredVars(subgraph, vars); err != nil {
		return HandleErrorWithHint(err.Error(), fmt.Sprintf("Provide them with: --var %s=<value>", firstMissingVar(subgraph, vars)))
	}

	if in.dryRun {
		renderWispCreateDryRun(protoID, subgraph, vars, in.rootOnly)
		return nil
	}

	result, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*InstantiateResult, string, error) {
		w := newUOWMolWriter(uw)
		spawnResult, err := cloneSubgraphInto(ctx, w, subgraph, CloneOptions{
			Vars:      vars,
			Actor:     actor,
			Ephemeral: true,
			Prefix:    types.IDPrefixWisp,
			RootOnly:  in.rootOnly,
		})
		if err != nil {
			return nil, "", fmt.Errorf("creating wisp: %w", err)
		}
		return spawnResult, fmt.Sprintf("bd: wisp create %s", protoID), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	return renderWispCreateResult(result)
}

func runWispListProxiedServer(ctx context.Context, showAll bool, typeFilter string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	r := uowMolReader{uw: uw}
	issues, err := r.SearchIssues(ctx, "", wispListFilter(typeFilter))
	if err != nil {
		return HandleError("listing wisps: %v", err)
	}

	return renderWispListResult(buildWispListResult(issues, showAll))
}

func runWispGCProxiedServer(ctx context.Context, dryRun bool, ageThreshold time.Duration, cleanAll, closedMode, force bool, excludeTypes []types.IssueType) error {
	CheckReadonly("wisp gc")

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	if closedMode {
		return runWispPurgeClosedProxiedServer(ctx, dryRun, force, excludeTypes)
	}

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	r := uowMolReader{uw: uw}
	abandoned, err := findAbandonedWisps(ctx, r, cleanAll, ageThreshold, excludeTypes)
	uw.Close(ctx)
	if err != nil && abandoned == nil {
		return HandleError("%v", err)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cascade expansion incomplete: %v\n", err)
	}

	if len(abandoned) == 0 {
		if jsonOutput {
			return outputJSON(WispGCResult{CleanedIDs: []string{}, CleanedCount: 0, DryRun: dryRun})
		}
		fmt.Println("No abandoned wisps found")
		return nil
	}

	if dryRun {
		if jsonOutput {
			ids := make([]string, len(abandoned))
			for i, o := range abandoned {
				ids[i] = o.ID
			}
			return outputJSON(WispGCResult{CleanedIDs: ids, Candidates: len(abandoned), CleanedCount: 0, DryRun: true})
		}
		fmt.Printf("Dry run: would clean %d abandoned wisp(s):\n\n", len(abandoned))
		for _, issue := range abandoned {
			age := formatTimeAgo(issue.UpdatedAt)
			fmt.Printf("  %s: %s (last updated: %s)\n", issue.ID, issue.Title, age)
		}
		fmt.Printf("\nRun without --dry-run to delete these wisps.\n")
		return nil
	}

	ids := make([]string, len(abandoned))
	for i, issue := range abandoned {
		ids[i] = issue.ID
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (domain.DeleteIssuesResult, string, error) {
		res, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
			IDs:                  ids,
			Cascade:              true,
			UpdateTextReferences: true,
		}, actor)
		if err != nil {
			return domain.DeleteIssuesResult{}, "", err
		}
		return res, fmt.Sprintf("bd: wisp gc %d", res.DeletedCount), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	return renderWispGCDeleteResult(ids, res)
}

func renderWispGCDeleteResult(ids []string, res domain.DeleteIssuesResult) error {
	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"deleted":              ids,
			"deleted_count":        res.DeletedCount,
			"dependencies_removed": res.DependenciesCount,
			"labels_removed":       res.LabelsCount,
			"events_removed":       res.EventsCount,
			"references_updated":   res.ReferencesUpdated,
			"orphaned_issues":      res.OrphanedIssues,
		})
	}
	fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), res.DeletedCount)
	fmt.Printf("  Removed %d dependency link(s)\n", res.DependenciesCount)
	fmt.Printf("  Removed %d label(s)\n", res.LabelsCount)
	fmt.Printf("  Removed %d event(s)\n", res.EventsCount)
	fmt.Printf("  Updated text references in %d issue(s)\n", res.ReferencesUpdated)
	if len(res.OrphanedIssues) > 0 {
		fmt.Printf("  %s Orphaned %d issue(s): %s\n",
			ui.RenderWarn("⚠"), len(res.OrphanedIssues), strings.Join(res.OrphanedIssues, ", "))
	}
	return nil
}

func runWispPurgeClosedProxiedServer(ctx context.Context, dryRun, force bool, excludeTypes []types.IssueType) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	r := uowMolReader{uw: uw}

	statusClosed := types.StatusClosed
	ephemeralTrue := true
	filter := types.IssueFilter{
		Status:       &statusClosed,
		Ephemeral:    &ephemeralTrue,
		ExcludeTypes: excludeTypes,
		Limit:        5000,
	}
	closedIssues, err := r.SearchIssues(ctx, "", filter)
	if err != nil {
		uw.Close(ctx)
		return HandleError("listing closed wisps: %v", err)
	}

	pinnedCount := 0
	infraCount := 0
	filtered := make([]*types.Issue, 0, len(closedIssues))
	for _, issue := range closedIssues {
		if issue.Pinned {
			pinnedCount++
			continue
		}
		if r.IsInfraTypeCtx(ctx, issue.IssueType) {
			infraCount++
			continue
		}
		filtered = append(filtered, issue)
	}
	closedIssues = filtered
	uw.Close(ctx)

	if pinnedCount > 0 && !jsonOutput {
		fmt.Printf("Skipping %d pinned issue(s) (protected from cleanup)\n", pinnedCount)
	}
	if infraCount > 0 && !jsonOutput {
		fmt.Printf("Skipping %d configured infra issue(s) protected from GC\n", infraCount)
	}

	if len(closedIssues) == 0 {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"deleted_count": 0,
				"message":       "No closed wisps to delete",
			})
		}
		fmt.Println("No closed wisps to delete")
		return nil
	}

	ids := make([]string, len(closedIssues))
	for i, issue := range closedIssues {
		ids[i] = issue.ID
	}

	if !force && !dryRun {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"candidates": len(ids),
				"dry_run":    true,
			})
		}
		fmt.Printf("Found %d closed wisp(s) to delete\n", len(ids))
		fmt.Printf("\nUse --force to proceed, or --dry-run for detailed preview.\n")
		return nil
	}

	if dryRun {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"candidates": len(ids),
				"dry_run":    true,
			})
		}
		fmt.Printf("Found %d closed wisp(s)\n", len(ids))
		fmt.Println("DRY RUN - no changes will be made")
		return nil
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (domain.DeleteIssuesResult, string, error) {
		res, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
			IDs:                  ids,
			Cascade:              true,
			UpdateTextReferences: true,
		}, actor)
		if err != nil {
			return domain.DeleteIssuesResult{}, "", err
		}
		return res, fmt.Sprintf("bd: wisp gc --closed %d", res.DeletedCount), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	return renderWispGCDeleteResult(ids, res)
}
