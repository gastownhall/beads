package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

type unclaimProxiedResult struct {
	unclaimed []*types.Issue
	ids       []string
	errs      []string
}

// runUnclaimProxiedServer releases claims through the proxied-server unit of
// work. expectedAssignee carries `--if-assignee`: a non-empty value selects the
// compare-and-swap release, and empty means an unconditional one. There is no
// third case — `--if-assignee ""` is rejected in unclaim.go before any issue is
// touched, precisely so an unset shell variable can never downgrade a CAS to an
// unconditional release.
//
// The two releases differ only in which use-case verb runs: both apply the same
// transition through the same classic issueops implementation, so a conditional
// release records the same "unclaimed" event and drops the same lease row as an
// unconditional one, on this backend exactly as on the embedded one.
//
// EXIT CONTRACT (unchanged by the port, and deliberately identical to the
// embedded path): a mismatched holder prints the storage.ErrAssigneeMismatch
// error naming the current holder, writes NOTHING, and exits 1 via SilentExit.
// `bd unclaim` has never had `bd update`'s ExitGuardMismatch(13) verdict —
// see the "Exit status" paragraph of the command's help — and this port does
// not invent one, because a proxied exit code that differs from the embedded
// one for the same refusal is exactly the divergence this lane exists to
// prevent.
func runUnclaimProxiedServer(ctx context.Context, args []string, reason string, force bool, expectedAssignee string) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (unclaimProxiedResult, string, error) {
		var r unclaimProxiedResult
		for _, id := range args {
			issue, _, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
			if errors.Is(rerr, storage.ErrNotFound) {
				r.errs = append(r.errs, fmt.Sprintf("Error resolving %s: not found", id))
				continue
			}
			if rerr != nil {
				r.errs = append(r.errs, fmt.Sprintf("Error resolving %s: %v", id, rerr))
				continue
			}
			fullID := issue.ID

			var uerr error
			if expectedAssignee != "" {
				uerr = uw.IssueUseCase().UnclaimIfAssignee(ctx, fullID, actor, expectedAssignee)
			} else {
				uerr = uw.IssueUseCase().Unclaim(ctx, fullID, actor, force)
			}
			if uerr != nil {
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

func runReclaimProxiedServer(ctx context.Context, olderThan time.Duration, filter types.ReclaimFilter) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	reclaimed, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) ([]types.ReclaimedLease, string, error) {
		out, rerr := uw.IssueUseCase().ReclaimExpiredLeases(ctx, olderThan, filter, actor)
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

	return renderReclaim(reclaimed, !filter.IsEmpty())
}
