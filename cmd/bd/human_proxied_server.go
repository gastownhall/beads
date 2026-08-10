package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

// The proxied-server twins of the four `bd human` subcommands. The direct
// route reaches storage through ensureStoreActive(), which in proxied mode
// would lazily open a DIRECT store and silently bypass the proxy (bd-m7zzd) —
// so each RunE branches here BEFORE that call. Reads ride one read-only unit
// of work; each write invocation is exactly ONE RunTx with a real commit
// message, per the proxied write convention.

// proxiedHumanIssues is the proxied-server side of humanIssues: the SAME
// humanListRequest, resolved against the workspace's own status/type config
// and queried through a unit of work. It goes through openAndPrepare rather
// than hand-building a filter so `bd human list` cannot mean one thing in
// direct mode and another behind the proxy — the divergence a second
// hand-built filter here would reintroduce the next time the defaults move.
func proxiedHumanIssues(ctx context.Context, status string) ([]*types.Issue, error) {
	uw, filter, err := openAndPrepare(ctx, listInput{ListRequest: humanListRequest(status)})
	if err != nil {
		return nil, err
	}
	defer uw.Close(ctx)

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// runHumanRespondProxiedServer takes the fully-formatted comment text, not the
// raw response: `bd human respond` resolves its text sources and applies the
// "Response: " shape once, so both backends store identically-shaped comments.
func runHumanRespondProxiedServer(ctx context.Context, issueID, commentText string) error {
	res, err := closeHumanProxied(ctx, issueID, commentText, "Responded", "human respond")
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if res.labelsKnown {
		warnIfNotHumanLabeled(res.issue)
	}
	fmt.Printf("%s Bead %s closed with response.\n", ui.RenderPass("✔"), res.issue.ID)
	return nil
}

// runHumanDismissProxiedServer takes the fully-formatted close reason, not the
// raw dismissal note: `bd human dismiss` resolves its text sources and applies
// the shared dismissedCloseReason prefix once for both backends.
func runHumanDismissProxiedServer(ctx context.Context, issueID, closeReason string) error {
	res, err := closeHumanProxied(ctx, issueID, "", closeReason, "human dismiss")
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if res.labelsKnown {
		warnIfNotHumanLabeled(res.issue)
	}
	fmt.Printf("%s Bead %s dismissed.\n", ui.RenderPass("✔"), res.issue.ID)
	return nil
}

// humanCloseResult carries the bead as read inside the close transaction, with
// its labels when they loaded, so the caller can warn once the transaction has
// committed. labelsKnown separates "loaded, and the label is absent" from "the
// load failed" — only the first is grounds for the advisory warning.
type humanCloseResult struct {
	issue       *types.Issue
	labelsKnown bool
}

// closeHumanProxied resolves a bead and closes it with closeReason inside ONE
// proxied-server transaction, adding comment first when it is non-empty, so
// the already-closed verdict and the close ride the same snapshot.
//
// It resolves through GetIssueOrWisp rather than proxiedRequireIssue because a
// human-labeled bead can be a WISP: `bd human list` shows the whole ephemeral
// plane, so a bead a person can see here must be one they can also answer. A
// wisp takes the wisp-side comment and close calls; the durable path is
// unchanged.
//
// Close hooks are NOT fired here. They used to need hand-wiring at each
// proxied call site, but the unit-of-work plumbing fires them itself now
// (uow.NewNotifyingProvider, bd-opisf) — buffered during the transaction and
// drained after Commit. A hand-wired call here would fire each hook twice.
func closeHumanProxied(ctx context.Context, id, comment, closeReason, commitVerb string) (humanCloseResult, error) {
	if uowProvider == nil {
		return humanCloseResult{}, errors.New("proxied-server UOW provider not initialized")
	}
	return uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (humanCloseResult, string, error) {
		src := workapi.NewUOWDetailSource(uw)
		issue, isWisp, err := workapi.GetIssueOrWisp(ctx, src, id)
		if errors.Is(err, storage.ErrNotFound) {
			return humanCloseResult{}, "", fmt.Errorf("issue not found: %s", id)
		}
		if err != nil {
			return humanCloseResult{}, "", fmt.Errorf("resolving issue ID %s: %w", id, err)
		}
		if issue.Status == types.StatusClosed {
			return humanCloseResult{}, "", fmt.Errorf("issue %s is already closed", issue.ID)
		}

		res := humanCloseResult{issue: issue}
		// Labels feed only the advisory human-label warning, so a failed load
		// means no warning — not a warning that the label is missing, which
		// is what ignoring the error used to produce.
		if labels, lerr := src.Labels(ctx, issue.ID, isWisp); lerr == nil {
			issue.Labels = labels
			res.labelsKnown = true
		}

		if comment != "" {
			var cerr error
			if isWisp {
				_, cerr = uw.CommentUseCase().AddCommentToWisp(ctx, issue.ID, actor, comment)
			} else {
				_, cerr = uw.CommentUseCase().AddCommentToIssue(ctx, issue.ID, actor, comment)
			}
			if cerr != nil {
				return humanCloseResult{}, "", fmt.Errorf("adding comment: %w", cerr)
			}
		}

		params := domain.CloseIssueParams{Reason: closeReason}
		if isWisp {
			_, err = uw.IssueUseCase().CloseWisp(ctx, issue.ID, params, actor)
		} else {
			_, err = uw.IssueUseCase().CloseIssue(ctx, issue.ID, params, actor)
		}
		if err != nil {
			return humanCloseResult{}, "", fmt.Errorf("closing bead: %w", err)
		}
		return res, fmt.Sprintf("bd: %s %s", commitVerb, issue.ID), nil
	})
}
