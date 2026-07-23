package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

type unclaimProxiedResult struct {
	unclaimed []*types.Issue
	ids       []string
	errs      []string
}

func runUnclaimProxiedServer(ctx context.Context, args []string, reason string, force bool) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (unclaimProxiedResult, string, error) {
		var r unclaimProxiedResult
		for _, id := range args {
			issue, _ := proxiedResolveIssueOrWisp(ctx, uw, id)
			if issue == nil {
				r.errs = append(r.errs, fmt.Sprintf("Error resolving %s: not found", id))
				continue
			}
			fullID := issue.ID

			if uerr := uw.IssueUseCase().Unclaim(ctx, fullID, actor, force); uerr != nil {
				r.errs = append(r.errs, fmt.Sprintf("Error unclaiming %s: %v", fullID, uerr))
				continue
			}

			if reason != "" {
				if _, cerr := uw.CommentUseCase().AddCommentToIssue(ctx, fullID, actor, reason); cerr != nil {
					r.errs = append(r.errs, fmt.Sprintf("Warning: failed to add reason comment on %s: %v", fullID, cerr))
				}
			}

			if jsonOutput {
				if updated, _ := uw.IssueUseCase().GetIssue(ctx, fullID); updated != nil {
					r.unclaimed = append(r.unclaimed, updated)
				}
			}
			r.ids = append(r.ids, fullID)
		}
		if len(r.ids) == 0 {
			return r, "", nil
		}
		return r, "bd: unclaim " + strings.Join(r.ids, ", "), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	for _, e := range res.errs {
		fmt.Fprintln(os.Stderr, e)
	}

	if len(res.ids) > 0 {
		commandDidWrite.Store(true)
	}

	if jsonOutput {
		if len(res.unclaimed) > 0 {
			if e := outputJSON(res.unclaimed); e != nil {
				return HandleError("%v", e)
			}
		}
	} else {
		reasonMsg := ""
		if reason != "" {
			reasonMsg = ": " + reason
		}
		for _, id := range res.ids {
			fmt.Printf("%s Unclaimed %s%s\n", ui.RenderPass("✓"), id, reasonMsg)
		}
	}

	if len(res.errs) > 0 {
		return SilentExit()
	}
	return nil
}

func runReclaimProxiedServer(ctx context.Context, olderThan time.Duration) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	reclaimed, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) ([]types.ReclaimedLease, string, error) {
		out, rerr := uw.IssueUseCase().ReclaimExpiredLeases(ctx, olderThan, actor)
		if rerr != nil {
			return nil, "", rerr
		}
		if len(out) == 0 {
			return out, "", nil
		}
		ids := make([]string, 0, len(out))
		for _, r := range out {
			ids = append(ids, r.ID)
		}
		return out, "bd: reclaim " + strings.Join(ids, ", "), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("reclaim: %v", err)
	}

	if len(reclaimed) > 0 {
		commandDidWrite.Store(true)
	}

	return renderReclaim(reclaimed)
}
