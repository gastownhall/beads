package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

type deferProxiedResult struct {
	issues []*types.Issue
	errs   []string

	// wasDeferred tracks, in lockstep with issues, whether each undeferred
	// issue was actually status=deferred (status flips to open) versus just
	// carrying a stray defer_until on some other status (status untouched,
	// only the timestamp cleared — ga-bq3w5). Unused by runDeferProxiedServer.
	wasDeferred []bool
}

func proxiedUpdateByID(ctx context.Context, uw uow.UnitOfWork, id string, isWisp bool, updates map[string]any) error {
	if isWisp {
		return uw.IssueUseCase().UpdateWisp(ctx, id, updates, actor)
	}
	return uw.IssueUseCase().UpdateIssue(ctx, id, updates, actor)
}

func proxiedGetByID(ctx context.Context, uw uow.UnitOfWork, id string, isWisp bool) *types.Issue {
	if isWisp {
		iss, _ := uw.IssueUseCase().GetWisp(ctx, id)
		return iss
	}
	iss, _ := uw.IssueUseCase().GetIssue(ctx, id)
	return iss
}

func runDeferProxiedServer(ctx context.Context, args []string, deferUntil *time.Time, reason string) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (deferProxiedResult, string, error) {
		var r deferProxiedResult
		for _, id := range args {
			issue, isWisp, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
			if errors.Is(rerr, storage.ErrNotFound) {
				r.errs = append(r.errs, fmt.Sprintf("Error resolving %s: not found", id))
				continue
			}
			if rerr != nil {
				r.errs = append(r.errs, fmt.Sprintf("Error resolving %s: %v", id, rerr))
				continue
			}
			fullID := issue.ID

			updates := map[string]interface{}{
				"status": string(types.StatusDeferred),
			}
			if deferUntil != nil {
				updates["defer_until"] = *deferUntil
			}
			if reason != "" {
				notes := issue.Notes
				if notes != "" {
					notes += "\n"
				}
				updates["notes"] = notes + reason
			}

			if uerr := proxiedUpdateByID(ctx, uw, fullID, isWisp, updates); uerr != nil {
				r.errs = append(r.errs, fmt.Sprintf("Error deferring %s: %v", fullID, uerr))
				continue
			}
			if updated := proxiedGetByID(ctx, uw, fullID, isWisp); updated != nil {
				r.issues = append(r.issues, updated)
			}
		}
		if len(r.issues) == 0 {
			return r, "", nil
		}
		return r, "bd: defer", nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	for _, e := range res.errs {
		fmt.Fprintln(os.Stderr, e)
	}

	if jsonOutput {
		if len(res.issues) > 0 {
			if e := outputJSON(res.issues); e != nil {
				return e
			}
		}
	} else {
		for _, iss := range res.issues {
			fmt.Printf("%s Deferred %s\n", ui.RenderAccent("*"), iss.ID)
		}
	}

	if len(args) > 0 {
		commandDidWrite.Store(true)
	}
	return nil
}

func runUndeferProxiedServer(ctx context.Context, args []string) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (deferProxiedResult, string, error) {
		var r deferProxiedResult
		for _, id := range args {
			issue, isWisp, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
			if errors.Is(rerr, storage.ErrNotFound) {
				r.errs = append(r.errs, fmt.Sprintf("Error getting %s: not found", id))
				continue
			}
			if rerr != nil {
				r.errs = append(r.errs, fmt.Sprintf("Error getting %s: %v", id, rerr))
				continue
			}
			fullID := issue.ID

			// Gate on defer_until, not status alone (ga-bq3w5) — mirrors the
			// embedded-backend fix in undefer.go. See that file's comment for
			// the full rationale.
			wasDeferred := issue.Status == types.StatusDeferred
			if !wasDeferred && issue.DeferUntil == nil {
				r.errs = append(r.errs, fmt.Sprintf("%s is not deferred (status: %s)", fullID, string(issue.Status)))
				continue
			}

			updates := map[string]interface{}{
				"defer_until": nil,
			}
			if wasDeferred {
				updates["status"] = string(types.StatusOpen)
			}
			if uerr := proxiedUpdateByID(ctx, uw, fullID, isWisp, updates); uerr != nil {
				r.errs = append(r.errs, fmt.Sprintf("Error undeferring %s: %v", fullID, uerr))
				continue
			}
			if updated := proxiedGetByID(ctx, uw, fullID, isWisp); updated != nil {
				r.issues = append(r.issues, updated)
				r.wasDeferred = append(r.wasDeferred, wasDeferred)
			}
		}
		if len(r.issues) == 0 {
			return r, "", nil
		}
		return r, "bd: undefer", nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	for _, e := range res.errs {
		fmt.Fprintln(os.Stderr, e)
	}

	if jsonOutput {
		if len(res.issues) > 0 {
			if e := outputJSON(res.issues); e != nil {
				return e
			}
		}
	} else {
		for i, iss := range res.issues {
			if i < len(res.wasDeferred) && res.wasDeferred[i] {
				fmt.Printf("%s Undeferred %s (now open)\n", ui.RenderPass("*"), iss.ID)
			} else {
				fmt.Printf("%s Cleared stale defer_until on %s (status unchanged: %s)\n", ui.RenderPass("*"), iss.ID, string(iss.Status))
			}
		}
	}

	if len(args) > 0 {
		commandDidWrite.Store(true)
	}
	return nil
}
