package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

type pourProxiedResult struct {
	spawn         *InstantiateResult
	totalAttached int
	attachCount   int
}

func runPourProxiedServer(ctx context.Context, in pourInput) error {
	CheckReadonly("pour")

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
		if sg.Phase == "vapor" {
			warnPourVaporFormula(in.protoArg, in.varFlags)
		}
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (pourProxiedResult, string, error) {
		w := newUOWMolWriter(uw)

		subgraph := formulaSubgraph
		protoID := formulaProtoID
		if subgraph == nil {
			resolvedID, err := utils.ResolvePartialID(ctx, w, in.protoArg)
			if err != nil {
				return pourProxiedResult{}, "", fmt.Errorf("%s not found as formula or proto ID", in.protoArg)
			}
			protoID = resolvedID
			protoIssue, err := w.GetIssue(ctx, protoID)
			if err != nil {
				return pourProxiedResult{}, "", fmt.Errorf("loading proto %s: %w", protoID, err)
			}
			if !isProto(protoIssue) {
				return pourProxiedResult{}, "", fmt.Errorf("%s is not a proto (missing '%s' label)", protoID, MoleculeLabel)
			}
			subgraph, err = loadTemplateSubgraph(ctx, w, protoID)
			if err != nil {
				return pourProxiedResult{}, "", fmt.Errorf("loading proto: %w", err)
			}
		}

		type attachmentInfo struct {
			id       string
			issue    *types.Issue
			subgraph *TemplateSubgraph
		}
		var attachments []attachmentInfo
		for _, attachArg := range in.attachArgs {
			attachID, err := utils.ResolvePartialID(ctx, w, attachArg)
			if err != nil {
				return pourProxiedResult{}, "", fmt.Errorf("resolving attachment ID %s: %w", attachArg, err)
			}
			attachIssue, err := w.GetIssue(ctx, attachID)
			if err != nil {
				return pourProxiedResult{}, "", fmt.Errorf("loading attachment %s: %w", attachID, err)
			}
			if !isProto(attachIssue) {
				return pourProxiedResult{}, "", fmt.Errorf("%s is not a proto (missing '%s' label)", attachID, MoleculeLabel)
			}
			attachSubgraph, err := loadTemplateSubgraph(ctx, w, attachID)
			if err != nil {
				return pourProxiedResult{}, "", fmt.Errorf("loading attachment subgraph %s: %w", attachID, err)
			}
			attachments = append(attachments, attachmentInfo{attachID, attachIssue, attachSubgraph})
		}

		vars := applyVariableDefaults(vars, subgraph)

		var attachSubgraphs []*TemplateSubgraph
		for _, a := range attachments {
			attachSubgraphs = append(attachSubgraphs, a.subgraph)
		}
		if err := checkPourVars(subgraph, attachSubgraphs, vars); err != nil {
			return pourProxiedResult{}, "", err
		}

		if in.dryRun {
			var previews []pourAttachPreview
			for _, a := range attachments {
				previews = append(previews, pourAttachPreview{title: a.issue.Title, steps: len(a.subgraph.Issues)})
			}
			renderPourDryRun(protoID, subgraph, vars, in.assignee, in.attachType, previews)
			return pourProxiedResult{}, "", nil
		}

		spawnResult, err := cloneSubgraphInto(ctx, w, subgraph, CloneOptions{
			Vars:     vars,
			Assignee: in.assignee,
			Actor:    actor,
			Prefix:   types.IDPrefixMol,
		})
		if err != nil {
			return pourProxiedResult{}, "", fmt.Errorf("pouring proto: %w", err)
		}

		totalAttached := 0
		if len(attachments) > 0 {
			spawnedMol, err := w.GetIssue(ctx, spawnResult.NewEpicID)
			if err != nil {
				return pourProxiedResult{}, "", fmt.Errorf("loading spawned mol: %w", err)
			}
			for _, attach := range attachments {
				bondResult, err := bondProtoMolAttachInto(ctx, w, attach.subgraph, attach.issue, spawnedMol, in.attachType, vars, "", actor, false, true)
				if err != nil {
					return pourProxiedResult{}, "", fmt.Errorf("attaching %s: %w", attach.id, err)
				}
				totalAttached += bondResult.Spawned
			}
		}

		return pourProxiedResult{spawn: spawnResult, totalAttached: totalAttached, attachCount: len(attachments)},
			fmt.Sprintf("bd: mol pour %s", protoID), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}
	if res.spawn == nil {
		return nil
	}

	return renderPourResult(res.spawn, res.totalAttached, res.attachCount)
}

func runMolShowProxiedServer(ctx context.Context, arg string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	r := uowMolReader{uw: uw}
	moleculeID, err := utils.ResolvePartialID(ctx, r, arg)
	if err != nil {
		return HandleErrorRespectJSON("molecule '%s' not found", arg)
	}

	subgraph, err := loadTemplateSubgraph(ctx, r, moleculeID)
	if err != nil {
		return HandleErrorRespectJSON("loading molecule: %v", err)
	}

	if molShowParallel {
		return showMoleculeWithParallel(subgraph)
	}
	return showMolecule(subgraph)
}

func runMolCurrentProxiedServer(ctx context.Context, args []string, agent string, limit int, rangeStr string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	r := uowMolReader{uw: uw}

	var rangeStart, rangeEnd int
	if rangeStr != "" {
		rangeStart, rangeEnd, err = parseRange(rangeStr)
		if err != nil {
			return HandleErrorRespectJSON("invalid range '%s': %v", rangeStr, err)
		}
	}
	explicitSteps := limit > 0 || rangeStr != ""

	var molecules []*MoleculeProgress

	if len(args) == 1 {
		moleculeID, err := utils.ResolvePartialID(ctx, r, args[0])
		if err != nil {
			return HandleErrorRespectJSON("molecule '%s' not found", args[0])
		}

		stats, err := r.GetMoleculeProgress(ctx, moleculeID)
		if err != nil {
			return HandleErrorRespectJSON("loading molecule: %v", err)
		}
		if stats.Total > LargeMoleculeThreshold && !explicitSteps && !jsonOutput {
			printLargeMoleculeSummary(stats)
			return nil
		}

		progress, err := getMoleculeProgress(ctx, r, moleculeID)
		if err != nil {
			return HandleErrorRespectJSON("loading molecule: %v", err)
		}
		if rangeStr != "" {
			progress.Steps = filterStepsByRange(progress.Steps, rangeStart, rangeEnd)
		} else if limit > 0 && len(progress.Steps) > limit {
			progress.Steps = progress.Steps[:limit]
		}
		molecules = append(molecules, progress)
	} else {
		molecules = findInProgressMolecules(ctx, r, agent)
		if len(molecules) == 0 {
			molecules = findHookedMolecules(ctx, r, agent)
		}
		if len(molecules) == 0 {
			if jsonOutput {
				return outputJSON([]interface{}{})
			}
			fmt.Printf("No molecules in progress")
			if agent != "" {
				fmt.Printf(" for %s", agent)
			}
			fmt.Println(".")
			fmt.Println("\nTo start work on a molecule:")
			fmt.Println("  bd mol wisp create <proto-id>  # Instantiate as ephemeral wisp")
			fmt.Println("  bd update <step-id> --claim  # Claim a step")
			return nil
		}
	}

	if jsonOutput {
		return outputJSON(molecules)
	}
	for i, mol := range molecules {
		if i > 0 {
			fmt.Println()
		}
		printMoleculeProgress(mol)
	}
	return nil
}

func runMolProgressProxiedServer(ctx context.Context, args []string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	r := uowMolReader{uw: uw}

	var moleculeID string
	if len(args) == 1 {
		resolved, err := utils.ResolvePartialID(ctx, r, args[0])
		if err != nil {
			return HandleErrorRespectJSON("molecule '%s' not found", args[0])
		}
		moleculeID = resolved
	} else {
		moleculeIDs := findInProgressMoleculeIDs(ctx, r, actor)
		if len(moleculeIDs) == 0 {
			if jsonOutput {
				return outputJSON([]interface{}{})
			}
			fmt.Println("No molecules in progress.")
			fmt.Println("\nUse: bd mol progress <molecule-id>")
			return nil
		}
		moleculeID = moleculeIDs[0]
	}

	stats, err := r.GetMoleculeProgress(ctx, moleculeID)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		output := map[string]interface{}{
			"molecule_id":     stats.MoleculeID,
			"molecule_title":  stats.MoleculeTitle,
			"total":           stats.Total,
			"completed":       stats.Completed,
			"in_progress":     stats.InProgress,
			"current_step_id": stats.CurrentStepID,
		}
		if stats.Total > 0 {
			output["percent"] = float64(stats.Completed) * 100 / float64(stats.Total)
		}
		return outputJSON(output)
	}

	printMoleculeProgressStats(stats)
	return nil
}

func runMolLastActivityProxiedServer(ctx context.Context, arg string) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	r := uowMolReader{uw: uw}
	moleculeID, err := utils.ResolvePartialID(ctx, r, arg)
	if err != nil {
		return HandleErrorRespectJSON("molecule '%s' not found", arg)
	}

	activity, err := r.GetMoleculeLastActivity(ctx, moleculeID)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		return outputJSON(activity)
	}
	fmt.Println(activity.LastActivity.UTC().Format(time.RFC3339))
	return nil
}

func runMolStaleProxiedServer(ctx context.Context, blockingOnly, unassignedOnly, showAll bool) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	result, err := findStaleMolecules(ctx, uowMolReader{uw: uw}, blockingOnly, unassignedOnly, showAll)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		return outputJSON(result)
	}
	renderStaleResult(result, blockingOnly)
	return nil
}

func runMolReadyGatedProxiedServer(ctx context.Context) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	molecules, err := findGateReadyMolecules(ctx, uowMolReader{uw: uw})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	return renderGatedReadyMolecules(molecules)
}

func runMolBondProxiedServer(ctx context.Context, in molBondInput) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	if in.dryRun {
		uw, err := proxiedOpenReadUOW(ctx)
		if err != nil {
			return err
		}
		defer uw.Close(ctx)
		r := uowMolReader{uw: uw}

		issueA, formulaA, err := resolveOrDescribe(ctx, r, in.argA)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		issueB, formulaB, err := resolveOrDescribe(ctx, r, in.argB)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		renderMolBondDryRun(in, issueA, formulaA, issueB, formulaB)
		return nil
	}

	type bondTxResult struct {
		result *BondResult
		idA    string
		idB    string
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (bondTxResult, string, error) {
		w := newUOWMolWriter(uw)

		subgraphA, cookedA, err := resolveOrCookToSubgraph(ctx, w, in.argA, in.vars)
		if err != nil {
			return bondTxResult{}, "", err
		}
		subgraphB, cookedB, err := resolveOrCookToSubgraph(ctx, w, in.argB, in.vars)
		if err != nil {
			return bondTxResult{}, "", err
		}

		issueA := subgraphA.Root
		issueB := subgraphB.Root
		aIsProto := issueA.IsTemplate || cookedA
		bIsProto := issueB.IsTemplate || cookedB

		var result *BondResult
		switch {
		case aIsProto && bIsProto:
			result, err = bondProtoProtoInto(ctx, w, issueA, issueB, in.bondType, in.customTitle, actor)
		case aIsProto && !bIsProto:
			result, err = bondProtoMolAttachInto(ctx, w, subgraphA, issueA, issueB, in.bondType, in.vars, in.childRef, actor, in.ephemeral, in.pour)
		case !aIsProto && bIsProto:
			result, err = bondProtoMolAttachInto(ctx, w, subgraphB, issueB, issueA, in.bondType, in.vars, in.childRef, actor, in.ephemeral, in.pour)
		default:
			result, err = bondMolMolInto(ctx, w, issueA, issueB, in.bondType, actor)
		}
		if err != nil {
			return bondTxResult{}, "", fmt.Errorf("bonding: %w", err)
		}
		return bondTxResult{result: result, idA: issueA.ID, idB: issueB.ID},
			fmt.Sprintf("bd: mol bond %s %s", issueA.ID, issueB.ID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	return renderMolBondResult(res.result, res.idA, res.idB, in.ephemeral, in.pour)
}

func runMolSquashProxiedServer(ctx context.Context, in molSquashInput) error {
	CheckReadonly("mol squash")

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	if in.dryRun {
		uw, err := proxiedOpenReadUOW(ctx)
		if err != nil {
			return err
		}
		defer uw.Close(ctx)
		r := uowMolReader{uw: uw}

		moleculeID, err := utils.ResolvePartialID(ctx, r, in.moleculeArg)
		if err != nil {
			return HandleErrorRespectJSON("resolving molecule ID %s: %v", in.moleculeArg, err)
		}
		subgraph, err := loadTemplateSubgraph(ctx, r, moleculeID)
		if err != nil {
			return HandleErrorRespectJSON("loading molecule: %v", err)
		}
		wispChildren := wispChildrenOf(subgraph)
		if len(wispChildren) == 0 {
			if jsonOutput {
				return outputJSON(SquashResult{MoleculeID: moleculeID, SquashedCount: 0})
			}
			fmt.Printf("No ephemeral children found for molecule %s\n", moleculeID)
			return nil
		}
		renderSquashDryRun(moleculeID, subgraph, wispChildren, in.keepChildren)
		return nil
	}

	result, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*SquashResult, string, error) {
		w := newUOWMolWriter(uw)

		moleculeID, err := utils.ResolvePartialID(ctx, w, in.moleculeArg)
		if err != nil {
			return nil, "", fmt.Errorf("resolving molecule ID %s: %w", in.moleculeArg, err)
		}
		subgraph, err := loadTemplateSubgraph(ctx, w, moleculeID)
		if err != nil {
			return nil, "", fmt.Errorf("loading molecule: %w", err)
		}
		wispChildren := wispChildrenOf(subgraph)
		if len(wispChildren) == 0 {
			return &SquashResult{MoleculeID: moleculeID, SquashedCount: 0}, "", nil
		}

		result, err := squashMoleculeInto(ctx, w, subgraph.Root, wispChildren, in.keepChildren, in.summary, actor)
		if err != nil {
			return nil, "", fmt.Errorf("squashing molecule: %w", err)
		}
		return result, fmt.Sprintf("bd: mol squash %s", moleculeID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if result.SquashedCount == 0 && result.DigestID == "" {
		if jsonOutput {
			return outputJSON(result)
		}
		fmt.Printf("No ephemeral children found for molecule %s\n", result.MoleculeID)
		return nil
	}

	return renderSquashResult(result)
}

func runMolBurnProxiedServer(ctx context.Context, args []string, dryRun, force bool) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	r := uowMolReader{uw: uw}

	var wispIDs []string
	var persistentIDs []string
	var failedResolve []string
	seenWispIDs := make(map[string]bool)
	for _, moleculeID := range args {
		resolvedID, err := utils.ResolvePartialID(ctx, r, moleculeID)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: failed to resolve %s: %v\n", moleculeID, err)
			}
			failedResolve = append(failedResolve, moleculeID)
			continue
		}
		issue, err := r.GetIssue(ctx, resolvedID)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", resolvedID, err)
			}
			failedResolve = append(failedResolve, moleculeID)
			continue
		}
		if issue.Ephemeral {
			subgraph, err := loadTemplateSubgraph(ctx, r, resolvedID)
			if err != nil {
				if !jsonOutput {
					fmt.Fprintf(os.Stderr, "Warning: failed to load wisp subgraph for %s: %v\n", resolvedID, err)
				}
				failedResolve = append(failedResolve, moleculeID)
				continue
			}
			for _, sgIssue := range subgraph.Issues {
				if sgIssue.Ephemeral && !seenWispIDs[sgIssue.ID] {
					seenWispIDs[sgIssue.ID] = true
					wispIDs = append(wispIDs, sgIssue.ID)
				}
			}
		} else {
			persistentIDs = append(persistentIDs, resolvedID)
		}
	}
	uw.Close(ctx)

	if len(wispIDs) == 0 && len(persistentIDs) == 0 {
		if jsonOutput {
			return outputJSON(BatchBurnResult{FailedCount: len(failedResolve)})
		}
		fmt.Println("No valid molecules to burn")
		return nil
	}

	if dryRun {
		if !jsonOutput {
			fmt.Printf("\nDry run: would burn %d wisp(s) and %d persistent molecule(s)\n", len(wispIDs), len(persistentIDs))
			if len(wispIDs) > 0 {
				fmt.Printf("\nWisps to delete:\n")
				for _, id := range wispIDs {
					fmt.Printf("  - %s\n", id)
				}
			}
			if len(persistentIDs) > 0 {
				fmt.Printf("\nPersistent molecules to delete:\n")
				for _, id := range persistentIDs {
					fmt.Printf("  - %s\n", id)
				}
			}
			if len(failedResolve) > 0 {
				fmt.Printf("\nFailed to resolve (%d):\n", len(failedResolve))
				for _, id := range failedResolve {
					fmt.Printf("  - %s\n", id)
				}
			}
		}
		return nil
	}

	if !force && !jsonOutput {
		fmt.Printf("About to burn %d wisp(s) and %d persistent molecule(s)\n", len(wispIDs), len(persistentIDs))
		fmt.Printf("This will permanently delete all molecule data with no digest.\n")
		fmt.Printf("\nContinue? [y/N] ")

		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Canceled.")
			return nil
		}
	}

	batchResult := BatchBurnResult{
		Results:     make([]BurnResult, 0),
		FailedCount: len(failedResolve),
	}

	if len(wispIDs) > 0 {
		result, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*BurnResult, string, error) {
			r, err := burnWispsInto(ctx, newUOWMolWriter(uw), wispIDs, actor)
			if err != nil {
				return nil, "", err
			}
			return r, fmt.Sprintf("bd: mol burn %d wisp(s)", len(wispIDs)), nil
		})
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Error burning wisps: %v\n", err)
			}
		} else {
			batchResult.TotalDeleted += result.DeletedCount
			batchResult.Results = append(batchResult.Results, *result)
		}
	}

	for _, id := range persistentIDs {
		result, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (BurnResult, string, error) {
			subgraph, err := loadTemplateSubgraph(ctx, newUOWMolWriter(uw), id)
			if err != nil {
				return BurnResult{}, "", fmt.Errorf("loading subgraph for %s: %w", id, err)
			}
			issueIDs := make([]string, len(subgraph.Issues))
			for i, issue := range subgraph.Issues {
				issueIDs[i] = issue.ID
			}
			res, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
				IDs:                  issueIDs,
				Cascade:              true,
				UpdateTextReferences: true,
			}, actor)
			if err != nil {
				return BurnResult{}, "", fmt.Errorf("burning %s: %w", id, err)
			}
			return BurnResult{
				MoleculeID:   id,
				DeletedIDs:   issueIDs,
				DeletedCount: res.DeletedCount,
			}, fmt.Sprintf("bd: mol burn %s", id), nil
		})
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: failed to burn %s: %v\n", id, err)
			}
			batchResult.FailedCount++
			continue
		}
		batchResult.TotalDeleted += result.DeletedCount
		batchResult.Results = append(batchResult.Results, result)
	}

	if jsonOutput {
		return outputJSON(batchResult)
	}
	fmt.Printf("%s Burned %d molecule(s): %d issues deleted\n", ui.RenderPass("✓"), len(wispIDs)+len(persistentIDs), batchResult.TotalDeleted)
	if batchResult.FailedCount > 0 {
		fmt.Printf("  %d failed\n", batchResult.FailedCount)
	}
	return nil
}

func runMolDistillProxiedServer(ctx context.Context, in molDistillInput) error {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	r := uowMolReader{uw: uw}
	epicID, err := utils.ResolvePartialID(ctx, r, in.epicID)
	if err != nil {
		return HandleErrorRespectJSON("'%s' not found", in.epicID)
	}

	subgraph, err := loadTemplateSubgraph(ctx, r, epicID)
	if err != nil {
		return HandleErrorRespectJSON("loading epic: %v", err)
	}

	return distillSubgraph(epicID, subgraph, in)
}
