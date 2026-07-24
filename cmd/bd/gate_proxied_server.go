package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
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
	defer uw.Close(ctx)
	return uw.IssueUseCase().GetIssue(ctx, id)
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

	for _, after := range applied.updated {
		if err := fireProxiedUpdateHook(ctx, after); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", after.ID, err)
		}
	}
	for _, c := range applied.closed {
		audit.LogFieldChange(c.after.ID, "status", c.oldStatus, "closed", actor, c.reason)
		if err := fireProxiedCloseHooks(ctx, c.before, c.after); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", c.after.ID, err)
		}
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

func fireProxiedUpdateHook(ctx context.Context, after *types.Issue) error {
	if after == nil {
		return nil
	}
	runner, err := proxiedHookRunner(ctx)
	if err != nil {
		return fmt.Errorf("hook runner: %w", err)
	}
	if runner == nil {
		return nil
	}
	if err := runner.RunSync(hooks.EventUpdate, after); err != nil {
		return fmt.Errorf("on_update hook: %w", err)
	}
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
		target, isWisp, err := proxiedGetIssueOrWisp(ctx, uw, args[0])
		if err != nil || target == nil {
			return HandleErrorRespectJSON("issue not found: %s", args[0])
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
