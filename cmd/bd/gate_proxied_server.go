package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

type proxiedGateClose struct {
	before    *types.Issue
	after     *types.Issue
	oldStatus string
	reason    string
}

type gateCheckApply struct {
	closed    []proxiedGateClose
	updated   []*types.Issue
	closeErrs map[string]error
	awaitErrs map[string]error
}

type proxiedFreshReadGetter struct{}

func (proxiedFreshReadGetter) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return nil, err
	}
	issue, localErr := uw.IssueUseCase().GetIssue(ctx, id)
	uw.Close(ctx)
	if localErr == nil {
		return issue, nil
	}
	if !gateProxiedNotFound(localErr) {
		return nil, localErr
	}

	result, routeErr := resolveViaPrefixRouting(ctx, id)
	if routeErr != nil {
		return nil, localErr
	}
	defer result.Close()
	return result.Issue, nil
}

func runGateCheckProxiedServer(cmd *cobra.Command, ctx context.Context) error {
	CheckReadonly("gate check")

	evt := metrics.NewCommandEvent("gate-check")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	gateTypeFilter, _ := cmd.Flags().GetString("type")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	escalateFlag, _ := cmd.Flags().GetBool("escalate")
	limit, _ := cmd.Flags().GetInt("limit")

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	gateType := types.IssueType("gate")
	filter := types.IssueFilter{
		IssueType:     &gateType,
		ExcludeStatus: []types.Status{types.StatusClosed},
		Limit:         limit,
	}

	discovered := map[string]string{}
	var persistAwaitID func(gateID, runID string) error
	if !dryRun {
		persistAwaitID = func(gateID, runID string) error {
			discovered[gateID] = runID
			return nil
		}
	}

	readUW, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	page, err := readUW.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		readUW.Close(ctx)
		return HandleErrorRespectJSON("%v", err)
	}
	filteredGates := filterCheckableGates(page.Items, gateTypeFilter)
	readUW.Close(ctx)

	if len(filteredGates) == 0 {
		printNoOpenGates(gateTypeFilter)
		return nil
	}
	results := evaluateGates(ctx, filteredGates, time.Now(), proxiedFreshReadGetter{}, persistAwaitID)

	if dryRun {
		resolved, escalated, errCount := applyGateCheckResults(results, true, escalateFlag, nil)
		return printGateCheckSummary(len(results), resolved, escalated, errCount, dryRun)
	}

	applied, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (gateCheckApply, string, error) {
		out := gateCheckApply{
			closeErrs: map[string]error{},
			awaitErrs: map[string]error{},
		}

		for gateID, runID := range discovered {
			if err := uw.IssueUseCase().UpdateIssue(ctx, gateID, map[string]any{"await_id": runID}, actor); err != nil {
				out.awaitErrs[gateID] = fmt.Errorf("failed to update gate with discovered run ID: %w", err)
				continue
			}
			if after, getErr := uw.IssueUseCase().GetIssue(ctx, gateID); getErr == nil && after != nil {
				out.updated = append(out.updated, after)
			}
		}

		for _, r := range results {
			if r.err != nil || !r.resolved {
				continue
			}
			if _, awaitFailed := out.awaitErrs[r.gate.ID]; awaitFailed {
				continue
			}
			before, _ := uw.IssueUseCase().GetIssue(ctx, r.gate.ID)
			if before != nil && before.Status == types.StatusClosed {
				continue
			}
			res, closeErr := uw.IssueUseCase().CloseIssue(ctx, r.gate.ID, domain.CloseIssueParams{Reason: r.reason}, actor)
			if closeErr != nil {
				out.closeErrs[r.gate.ID] = closeErr
				continue
			}
			oldStatus := "open"
			if before != nil && before.Status != "" {
				oldStatus = string(before.Status)
			}
			out.closed = append(out.closed, proxiedGateClose{
				before:    before,
				after:     res.Issue,
				oldStatus: oldStatus,
				reason:    r.reason,
			})
		}

		return out, "bd: gate check", nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	for _, c := range applied.closed {
		audit.LogFieldChange(c.after.ID, "status", c.oldStatus, "closed", actor, c.reason)
	}
	if len(applied.closed) > 0 || len(applied.updated) > 0 {
		commandDidWrite.Store(true)
	}

	for i := range results {
		if awaitErr, failed := applied.awaitErrs[results[i].gate.ID]; failed {
			results[i].resolved = false
			results[i].escalated = false
			results[i].err = awaitErr
		}
	}

	resolved, escalated, errCount := applyGateCheckResults(results, false, escalateFlag,
		func(gate *types.Issue, reason string) error {
			return applied.closeErrs[gate.ID]
		})
	return printGateCheckSummary(len(results), resolved, escalated, errCount, dryRun)
}

// gateProxiedNotFound reports whether an issue lookup failed because the row
// does not exist, as opposed to the read itself failing. The distinction is
// what keeps `bd gate show` fail-closed: "no gate" and "could not read" must
// not collapse into one message.
func gateProxiedNotFound(err error) bool {
	return errors.Is(err, storage.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}

func runGateShowProxiedServer(_ *cobra.Command, ctx context.Context, args []string) error {
	evt := metrics.NewCommandEvent("gate-show")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	gateID := args[0]

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	issue, err := uw.IssueUseCase().GetIssue(ctx, gateID)
	if gateProxiedNotFound(err) {
		return HandleErrorRespectJSON("gate not found: %s", gateID)
	}
	if err != nil {
		// A failed read is not "no gate": it exits nonzero with its own
		// message so a caller grepping the output cannot mistake an
		// unreachable server for a missing gate.
		return HandleErrorRespectJSON("reading gate %s: %v", gateID, err)
	}

	if issue.IssueType != "gate" {
		return HandleErrorRespectJSON("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
	}

	if jsonOutput {
		return outputJSON(issue)
	}

	renderGateShow(issue)
	return nil
}

type gateAddWaiterApply struct {
	already bool
	after   *types.Issue
}

func runGateAddWaiterProxiedServer(_ *cobra.Command, ctx context.Context, args []string) error {
	CheckReadonly("gate add-waiter")

	evt := metrics.NewCommandEvent("gate-add-waiter")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	gateID := args[0]
	waiter := args[1]

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	applied, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (gateAddWaiterApply, string, error) {
		var out gateAddWaiterApply

		issue, err := uw.IssueUseCase().GetIssue(ctx, gateID)
		if gateProxiedNotFound(err) {
			return out, "", fmt.Errorf("gate not found: %s", gateID)
		}
		if err != nil {
			return out, "", fmt.Errorf("reading gate %s: %w", gateID, err)
		}
		if issue.IssueType != "gate" {
			return out, "", fmt.Errorf("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
		}

		for _, w := range issue.Waiters {
			if w == waiter {
				out.already = true
				// Empty commit message: a registered waiter is a no-op, and a
				// no-op writes no Dolt commit.
				return out, "", nil
			}
		}

		newWaiters := append(issue.Waiters, waiter)
		if err := uw.IssueUseCase().UpdateIssue(ctx, gateID, map[string]any{"waiters": newWaiters}, actor); err != nil {
			return out, "", fmt.Errorf("updating gate: %w", err)
		}
		if after, getErr := uw.IssueUseCase().GetIssue(ctx, gateID); getErr == nil {
			out.after = after
		}
		return out, fmt.Sprintf("bd: gate add-waiter %s", gateID), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	if applied.already {
		renderGateWaiterAlready(gateID)
		return nil
	}

	commandDidWrite.Store(true)

	renderGateWaiterAdded(gateID, waiter)
	return nil
}

type gateCreateApply struct {
	gate   *types.Issue
	target *types.Issue
}

func runGateCreateProxiedServer(cmd *cobra.Command, ctx context.Context) error {
	CheckReadonly("gate create")

	evt := metrics.NewCommandEvent("gate-create")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	in, err := gatherGateCreateInput(cmd)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	// One transaction, one Dolt commit for the whole invocation: the direct
	// route's create + add-dependency + explicit store.Commit collapse into a
	// single unit of work carrying the same commit message. Semantically
	// equivalent, minus the window where the gate exists without its edge.
	applied, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (gateCreateApply, string, error) {
		var out gateCreateApply

		target, err := uw.IssueUseCase().GetIssue(ctx, in.blocksID)
		if err != nil {
			// The direct route reports every target-lookup failure as
			// not-found; keep that message for parity.
			return out, "", fmt.Errorf("issue not found: %s", in.blocksID)
		}

		gate := buildGateIssue(in, target.ID)
		metadata, metaErr := repoMetadataForGate(in.gateType, target)
		if metaErr != nil {
			return out, "", fmt.Errorf("invalid GitHub repository metadata on %s: %v", target.ID, metaErr)
		}
		gate.Metadata = metadata

		res, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: gate}, actor)
		if err != nil {
			return out, "", fmt.Errorf("creating gate: %w", err)
		}

		dep := &types.Dependency{
			IssueID:     target.ID,
			DependsOnID: res.Issue.ID,
			Type:        types.DepBlocks,
		}
		if err := uw.DependencyUseCase().AddDependency(ctx, dep, actor); err != nil {
			return out, "", fmt.Errorf("adding blocking dependency: %w", err)
		}

		out.gate = res.Issue
		out.target = target
		return out, fmt.Sprintf("bd: create gate %s blocking %s", res.Issue.ID, target.ID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	commandDidWrite.Store(true)

	if jsonOutput {
		return outputJSON(applied.gate)
	}

	renderGateCreated(applied.gate, applied.target, in)
	return nil
}

type gateResolveApply struct {
	before    *types.Issue
	after     *types.Issue
	oldStatus string
	closed    bool // CloseIssueResult.Closed: false when the gate was already closed
}

func runGateResolveProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	CheckReadonly("gate resolve")

	evt := metrics.NewCommandEvent("gate-resolve")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	gateID := args[0]
	reason, _ := cmd.Flags().GetString("reason")

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	applied, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (gateResolveApply, string, error) {
		var out gateResolveApply

		issue, err := uw.IssueUseCase().GetIssue(ctx, gateID)
		if gateProxiedNotFound(err) {
			return out, "", fmt.Errorf("gate not found: %s", gateID)
		}
		if err != nil {
			return out, "", fmt.Errorf("reading gate %s: %w", gateID, err)
		}
		if issue.IssueType != "gate" {
			return out, "", fmt.Errorf("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
		}

		res, err := uw.IssueUseCase().CloseIssue(ctx, gateID, domain.CloseIssueParams{Reason: reason}, actor)
		if err != nil {
			return out, "", fmt.Errorf("closing gate: %w", err)
		}

		out.before = issue
		out.after = res.Issue
		out.oldStatus = "open"
		if issue.Status != "" {
			out.oldStatus = string(issue.Status)
		}
		out.closed = res.Closed
		return out, fmt.Sprintf("bd: gate resolve %s", gateID), nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	// Audit only when this invocation actually closed the gate — a
	// double-resolve must not re-log it (same guard as the o.closed check in
	// close_proxied_server.go).
	if applied.closed && applied.after != nil {
		audit.LogFieldChange(applied.after.ID, "status", applied.oldStatus, "closed", actor, reason)
	}
	commandDidWrite.Store(true)

	renderGateResolved(gateID, reason)
	return nil
}

func runGateListProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	allFlag, _ := cmd.Flags().GetBool("all")
	limit, _ := cmd.Flags().GetInt("limit")

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if len(args) == 1 {
		target, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), args[0])
		if errors.Is(err, storage.ErrNotFound) {
			return HandleErrorRespectJSON("issue not found: %s", args[0])
		}
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		var metas []*types.IssueWithDependencyMetadata
		if isWisp {
			metas, err = uw.DependencyUseCase().ListWispWithIssueMetadata(ctx, target.ID, domain.DepListFilter{Direction: domain.DepDirectionOut})
		} else {
			metas, err = uw.DependencyUseCase().ListWithIssueMetadata(ctx, target.ID, domain.DepListFilter{Direction: domain.DepDirectionOut})
		}
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		deps := make([]*types.Issue, 0, len(metas))
		for _, m := range metas {
			if m != nil {
				deps = append(deps, &m.Issue)
			}
		}
		gates := filterIssueGates(deps, allFlag, limit)
		if jsonOutput {
			return outputJSON(gates)
		}
		displayGates(gates, allFlag)
		return nil
	}

	gateType := types.IssueType("gate")
	filter := types.IssueFilter{
		IssueType: &gateType,
		Limit:     limit,
	}
	if !allFlag {
		filter.ExcludeStatus = []types.Status{types.StatusClosed}
	}
	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if jsonOutput {
		return outputJSON(page.Items)
	}
	displayGates(page.Items, allFlag)
	return nil
}
