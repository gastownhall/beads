package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/remedy"
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
	// Explicit commit point, like the `transact` in cloneSubgraph the direct
	// route reaches through spawnMolecule (GH#4995).
	ctx = explicitCommitPointContext(ctx)

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
	} else if errors.Is(err, formula.ErrVarValidation) || errors.Is(err, formula.ErrValidation) {
		// in.protoArg IS a formula; either the formula itself does not
		// validate, or the --var values it was given fail enum/pattern/
		// required-empty constraints. Report that directly
		// instead of falling through to the proto-ID lookup below, which
		// would otherwise mask this as "not found as formula or proto ID".
		return HandleError("%v", err)
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
			Actor:    currentActor(),
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
				bondResult, err := bondProtoMolAttachInto(ctx, w, attach.subgraph, attach.issue, spawnedMol, in.attachType, vars, "", currentActor(), false, true)
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
	moleculeID, err := proxiedResolvePartialID(ctx, arg)
	if err != nil {
		return moleculeNotResolved(arg, err)
	}

	roles, err := proxiedMoleculeRoles()
	if err != nil {
		return HandleErrorRespectJSON("loading molecule: %v", err)
	}
	return runMolShow(ctx, roles, moleculeID)
}

func runMolCurrentProxiedServer(ctx context.Context, args []string, agent string, limit int, rangeStr string) error {
	return runMolCurrent(ctx, nil, proxiedResolvePartialID, args, agent, limit, rangeStr)
}

func runMolProgressProxiedServer(ctx context.Context, args []string) error {
	return runMolProgress(ctx, nil, proxiedResolvePartialID, args)
}

// proxiedResolvePartialID resolves a partial id inside a read unit of work of
// its own. A unit of work that cannot be opened is reported as itself
// (proxiedOpenReadUOW) and answered as a reportedResolveError, so the caller
// does not report a connection failure as a molecule that was not found.
func proxiedResolvePartialID(ctx context.Context, arg string) (string, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return "", reportedResolveError{err: err}
	}
	defer uw.Close(ctx)
	return utils.ResolvePartialID(ctx, uowMolReader{uw: uw}, arg)
}

// reportedResolveError is an id resolution that failed before it could look
// the id up, and whose failure was already reported.
type reportedResolveError struct{ err error }

func (e reportedResolveError) Error() string { return e.err.Error() }
func (e reportedResolveError) Unwrap() error { return e.err }

// moleculeNotResolved reports a molecule id argument that did not resolve:
// "not found" when the lookup ran, and the failure as already reported when
// the lookup never ran (reportedResolveError).
func moleculeNotResolved(arg string, err error) error {
	var reported reportedResolveError
	if errors.As(err, &reported) {
		return reported.err
	}
	return HandleErrorRespectJSON("molecule '%s' not found", arg)
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

// proxiedServedHomeKey is the logical home of an issue the proxied-server
// launcher's own database serves. It is no store's dependencyStoreKey, so a
// served issue and a routed one never pass as one home.
const proxiedServedHomeKey = "proxied-server:launcher"

// molBondReopenRoutedHome reopens a planned routed bond home writable. It is a
// variable so a test can hand back a handle onto another store.
var molBondReopenRoutedHome = func(ctx context.Context, id string) (*RoutedResult, error) {
	return resolveViaPrefixRoutingWithAccess(ctx, id, true, true)
}

func runMolBondProxiedServer(ctx context.Context, in molBondInput) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	// Explicit commit point, like `transact` in bondProtoMol/bondMolecules on
	// the direct route (GH#4995).
	ctx = explicitCommitPointContext(ctx)

	// Plan read-only, as the direct route does: both operands are discovered
	// (served database, then prefix routes, then formulas) and must share one
	// home before anything is opened for writing.
	plan, err := planMolBondProxied(ctx, in)
	if err != nil {
		if isReportedExit(err) {
			// A provider-open failure reported itself (proxiedOpenReadUOW).
			return err
		}
		return HandleErrorRespectJSON("%v", err)
	}
	defer plan.Close()

	if in.dryRun {
		renderMolBondDryRun(in, plan.a.issue, plan.a.formula, plan.b.issue, plan.b.formula)
		return nil
	}
	plan.Close()

	if plan.targetStoreKey != proxiedServedHomeKey {
		// A routed rig owns the bond: write it there, in that store's own
		// transaction, never through the launcher's unit of work.
		home, closeHome, err := reopenMolBondHome(plan, func() (*RoutedResult, error) {
			return molBondReopenRoutedHome(ctx, plan.targetID)
		})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		defer closeHome()
		return executeMolBond(ctx, home, plan, in)
	}

	type bondTxResult struct {
		result *BondResult
		idA    string
		idB    string
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (bondTxResult, string, error) {
		w := newUOWMolWriter(uw)

		opA, err := materializeServedMolBondOperand(ctx, w, plan.a, in.vars)
		if err != nil {
			return bondTxResult{}, "", err
		}
		opB, err := materializeServedMolBondOperand(ctx, w, plan.b, in.vars)
		if err != nil {
			return bondTxResult{}, "", err
		}

		subgraphA, subgraphB := opA.subgraph, opB.subgraph
		issueA := subgraphA.Root
		issueB := subgraphB.Root
		aIsProto := opA.bondsAsProto()
		bIsProto := opB.bondsAsProto()

		var result *BondResult
		switch {
		case aIsProto && bIsProto:
			result, err = bondProtoProtoInto(ctx, w, issueA, issueB, in.bondType, in.customTitle, currentActor())
		case aIsProto && !bIsProto:
			result, err = bondProtoMolAttachInto(ctx, w, subgraphA, issueA, issueB, in.bondType, in.vars, in.childRef, currentActor(), in.ephemeral, in.pour)
		case !aIsProto && bIsProto:
			result, err = bondProtoMolAttachInto(ctx, w, subgraphB, issueB, issueA, in.bondType, in.vars, in.childRef, currentActor(), in.ephemeral, in.pour)
		default:
			result, err = bondMolMolInto(ctx, w, issueA, issueB, in.bondType, currentActor())
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

// planMolBondProxied is planMolBond for a proxied-server launcher, which has
// no local store: a formula-only bond stays in the served database.
func planMolBondProxied(ctx context.Context, in molBondInput) (*molBondPlan, error) {
	return planMolBondWith(in, proxiedServedHomeKey, func(operand string) (*molBondDiscovery, error) {
		return discoverMolBondOperandProxied(ctx, operand, in.vars)
	})
}

// discoverMolBondOperandProxied resolves an operand issue-first: in the served
// database through a short-lived read unit of work, then, when the served
// database does not have it, through a prefix route opened read-only. A read
// that will not open fails the bond; any other miss goes to the formula
// fallback, which keeps an actionable route error when no formula matches.
func discoverMolBondOperandProxied(ctx context.Context, operand string, vars map[string]string) (*molBondDiscovery, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return nil, err
	}
	issue, issueErr := resolveServedMolBondIssue(ctx, uowMolReader{uw: uw}, operand)
	uw.Close(ctx)
	if issueErr == nil {
		return &molBondDiscovery{operand: operand, issue: issue, storeKey: proxiedServedHomeKey}, nil
	}

	if isNotFoundErr(issueErr) {
		rr, routeErr := resolveViaPrefixRoutingWithAccess(ctx, operand, false, false)
		if routeErr == nil {
			return &molBondDiscovery{
				operand:  operand,
				issue:    rr.Issue,
				store:    rr.Store,
				storeKey: dependencyStoreKey(rr.Store),
				closeFn:  rr.Close,
			}, nil
		}
		if isRoutingFailure(routeErr) {
			issueErr = routeErr
		}
	}
	return discoverMolBondFormula(operand, vars, issueErr)
}

// resolveServedMolBondIssue resolves operand to an issue in the served
// database. An ID the served database does not match reads as not found
// (isNotFoundErr), so a prefix route may still have it.
func resolveServedMolBondIssue(ctx context.Context, r molReader, operand string) (*types.Issue, error) {
	id, err := utils.ResolvePartialID(ctx, r, operand)
	if err != nil {
		return nil, err
	}
	return r.GetIssue(ctx, id)
}

// materializeServedMolBondOperand re-reads a planned operand inside the
// served write transaction by its exact planned ID, or cooks its formula. A
// planned issue that is gone fails the bond: it is not re-resolved and never
// falls back to a same-named formula.
func materializeServedMolBondOperand(ctx context.Context, r molReader, d *molBondDiscovery, vars map[string]string) (*molBondOperand, error) {
	if d.issue == nil {
		return cookMolBondFormulaOperand(d.operand, vars)
	}
	issue, err := r.GetIssue(ctx, d.issue.ID)
	if err != nil {
		return nil, err
	}
	if isProto(issue) {
		subgraph, err := loadTemplateSubgraph(ctx, r, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("loading proto subgraph '%s': %w", issue.ID, err)
		}
		return &molBondOperand{subgraph: subgraph}, nil
	}
	return &molBondOperand{
		subgraph: &TemplateSubgraph{
			Root:     issue,
			Issues:   []*types.Issue{issue},
			IssueMap: map[string]*types.Issue{issue.ID: issue},
		},
	}, nil
}

func runMolSquashProxiedServer(ctx context.Context, in molSquashInput) error {
	CheckReadonly("mol squash")

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}
	// Explicit commit point, like `transact` in runMolSquash on the direct
	// route (GH#4995).
	ctx = explicitCommitPointContext(ctx)

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

		result, err := squashMoleculeInto(ctx, w, subgraph.Root, wispChildren, in.keepChildren, in.summary, currentActor())
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
	// Only the wisp half is an explicit commit point, so the exemption is
	// applied per commit site below rather than to this ctx (GH#4995).
	// runMolBurn splits on the direct route: wisps go through burnWisps ->
	// `transact`, which mints the commit whatever dolt.auto-commit says, while
	// persistent molecules go through deleteBatch -> issueOpsContext, the
	// policy-honoring class — dolt's deleter returns before DOLT_COMMIT when
	// that context defers. Exempting the whole command would mint one commit
	// per persistent molecule under batch/off, on a destructive verb, where the
	// identical direct-route command defers them all to `bd dolt commit`.

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
				fmt.Fprintf(os.Stderr, "Warning: failed to resolve %s: %s\n", moleculeID, remedy.PrintedMessage(err))
			}
			failedResolve = append(failedResolve, moleculeID)
			continue
		}
		issue, err := r.GetIssue(ctx, resolvedID)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %s\n", resolvedID, remedy.PrintedMessage(err))
			}
			failedResolve = append(failedResolve, moleculeID)
			continue
		}
		if issue.Ephemeral {
			subgraph, err := loadTemplateSubgraph(ctx, r, resolvedID)
			if err != nil {
				if !jsonOutput {
					fmt.Fprintf(os.Stderr, "Warning: failed to load wisp subgraph for %s: %s\n", resolvedID, remedy.PrintedMessage(err))
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
		// The wisp half's direct twin is burnWisps -> `transact`.
		result, err := uow.RunTxResult(explicitCommitPointContext(ctx), uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*BurnResult, string, error) {
			r, err := burnWispsInto(ctx, newUOWMolWriter(uw), wispIDs, currentActor())
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
		// No exemption: the persistent half's direct twin is deleteBatch ->
		// issueOpsContext, so this stays on the inherited policy context and
		// the message is blanked under batch/off, exactly as the direct route
		// leaves these deletions in the working set for `bd dolt commit`.
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
			}, currentActor())
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
