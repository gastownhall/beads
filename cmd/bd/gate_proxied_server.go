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
	seenErrs  map[string]error
}

// proxiedFreshReadGetter reads bead gate targets in proxied-server mode, each
// lookup in a fresh read transaction. A target can be an issue or a wisp. A
// local miss falls back to prefix routing like the direct getter; proxied
// mode has no contributor auto-routing.
type proxiedFreshReadGetter struct{}

func (g proxiedFreshReadGetter) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	issue, _, err := g.getBeadGateTarget(ctx, id)
	return issue, err
}

func (proxiedFreshReadGetter) getBeadGateTarget(ctx context.Context, id string) (*types.Issue, bool, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return nil, false, err
	}
	issue, _, localErr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
	uw.Close(ctx)
	if localErr == nil {
		return issue, true, nil
	}
	if !gateProxiedNotFound(localErr) {
		return nil, false, localErr
	}

	routed, routeErr := prefixRoutedBeadGateTarget(ctx, id)
	if routeErr != nil {
		return nil, false, routeErr
	}
	if routed != nil {
		return routed, false, nil
	}
	return nil, true, localErr
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

	discovered := map[string]string{}
	var seen []*types.Issue
	var persistAwaitID func(gateID, runID string) error
	var recordSeen func(gate *types.Issue) error
	if !dryRun {
		persistAwaitID = func(gateID, runID string) error {
			discovered[gateID] = runID
			return nil
		}
		recordSeen = func(gate *types.Issue) error {
			seen = append(seen, gate)
			return nil
		}
	}

	// The listing is the shared entry's, on this route too (gate list's S20).
	gates, err := listOpenGatesForCheck(ctx, limit)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	filteredGates := filterCheckableGates(gates, gateTypeFilter)

	if len(filteredGates) == 0 {
		printNoOpenGates(gateTypeFilter)
		return nil
	}
	results := evaluateGates(ctx, filteredGates, time.Now(), proxiedFreshReadGetter{}, persistAwaitID, recordSeen)

	if dryRun {
		resolved, escalated, errCount := applyGateCheckResults(results, true, escalateFlag, nil)
		return printGateCheckSummary(len(results), resolved, escalated, errCount, dryRun)
	}

	applied, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (gateCheckApply, string, error) {
		out := gateCheckApply{
			closeErrs: map[string]error{},
			awaitErrs: map[string]error{},
			seenErrs:  map[string]error{},
		}

		for gateID, runID := range discovered {
			if err := uw.IssueUseCase().UpdateIssue(ctx, gateID, map[string]any{"await_id": runID}, currentActor()); err != nil {
				out.awaitErrs[gateID] = fmt.Errorf("failed to update gate with discovered run ID: %w", err)
				continue
			}
			if after, getErr := uw.IssueUseCase().GetIssue(ctx, gateID); getErr == nil && after != nil {
				out.updated = append(out.updated, after)
			}
		}

		for _, gate := range seen {
			if err := recordBeadGateSeenProxied(ctx, uw, gate); err != nil {
				out.seenErrs[gate.ID] = err
				continue
			}
			out.updated = append(out.updated, gate)
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
			res, closeErr := uw.IssueUseCase().CloseIssue(ctx, r.gate.ID, domain.CloseIssueParams{Reason: r.reason}, currentActor())
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
		audit.LogFieldChange(c.after.ID, "status", c.oldStatus, "closed", currentActor(), c.reason)
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
		if seenErr, failed := applied.seenErrs[results[i].gate.ID]; failed {
			results[i].resolved = false
			results[i].escalated = false
			results[i].err = seenErr
		}
	}

	resolved, escalated, errCount := applyGateCheckResults(results, false, escalateFlag,
		func(gate *types.Issue, reason string) error {
			return applied.closeErrs[gate.ID]
		})
	return printGateCheckSummary(len(results), resolved, escalated, errCount, dryRun)
}

// recordBeadGateSeenProxied is the proxied-server form of the direct route's
// recordSeen write: it stamps beadGateSeenKey on a bead gate, which may be an
// issue or a wisp.
func recordBeadGateSeenProxied(ctx context.Context, uw uow.UnitOfWork, gate *types.Issue) error {
	_, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), gate.ID)
	if err == nil {
		err = proxiedUpdateByID(ctx, uw, gate.ID, isWisp, beadGateSeenUpdate(gate.AwaitID))
	}
	if err != nil {
		targetID, _ := beadGateTargetID(gate.AwaitID)
		return fmt.Errorf("recording that bead %s exists: %w", targetID, err)
	}
	return nil
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

		res, err := uw.IssueUseCase().CloseIssue(ctx, gateID, domain.CloseIssueParams{Reason: reason}, currentActor())
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
		audit.LogFieldChange(applied.after.ID, "status", applied.oldStatus, "closed", currentActor(), reason)
	}
	commandDidWrite.Store(true)

	renderGateResolved(gateID, reason)
	return nil
}

func runGateListProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	allFlag, _ := cmd.Flags().GetBool("all")
	limit, _ := cmd.Flags().GetInt("limit")

	if len(args) == 0 {
		// The workspace-wide listing is the shared entry's, on this route too.
		return runGateList(ctx, allFlag, limit)
	}

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

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
