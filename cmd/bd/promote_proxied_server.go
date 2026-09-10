package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/workapi"
)

// runPromoteProxiedServer is the proxied-server route for `bd promote`. It
// mirrors the classic route: resolve the id, refuse a non-wisp with the same
// error text, then promote through the domain PromoteWisp verb — which runs
// the exact issueops implementation the classic route runs, so the row is
// rewritten in place (same id, wisp_type retained, labels/deps/events/
// comments carried to the permanent tables, inbound edges retargeted).
//
// Commit semantics: the cross-plane move and the promotion comment land in
// ONE transaction under one Dolt commit ("bd: promote <id>", the classic
// dolt commit message). The wisp-side deletes touch dolt_ignored tables so
// only the issues-plane rows are versioned by that commit, but the single
// transaction still makes the wisp delete and the issues insert atomic.
//
// One deliberate tightening vs classic: classic adds the promotion comment
// in a separate write after the promote has committed, so a failed comment
// write can only degrade to a stderr warning. Here the comment shares the
// promote's transaction, so a comment failure rolls the whole promote back —
// atomic beats warn-and-continue, and a comment insert on a row this same
// transaction just promoted has no realistic standalone failure mode.
func runPromoteProxiedServer(ctx context.Context, id, reason string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	// Resolve + precondition checks on a read UOW, mirroring the classic
	// route's resolve/get/ephemeral-check before its write transaction. The
	// promote below re-verifies the wisp is still active inside the write
	// transaction (a lost race fails with "wisp <id> not found", as classic).
	ruw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	fullID, err := utils.ResolvePartialID(ctx, uowMolReader{uw: ruw}, id)
	if err != nil {
		ruw.Close(ctx)
		return HandleErrorRespectJSON("resolving %s: %v", id, err)
	}
	issue, _, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(ruw), fullID)
	ruw.Close(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return HandleErrorRespectJSON("issue %s not found", fullID)
		}
		return HandleErrorRespectJSON("getting issue %s: %v", fullID, err)
	}
	if !issue.Ephemeral {
		return HandleErrorRespectJSON("%s is not a wisp (already persistent)", fullID)
	}

	err = uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		if err := uw.IssueUseCase().PromoteWisp(ctx, fullID, actor); err != nil {
			return "", err
		}
		if _, err := uw.CommentUseCase().AddCommentToIssue(ctx, fullID, actor, promotionComment(reason)); err != nil {
			return "", fmt.Errorf("adding promotion comment: %w", err)
		}
		return fmt.Sprintf("bd: promote %s", fullID), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("promoting %s: %v", fullID, err)
	}

	commandDidWrite.Store(true)

	var updated *types.Issue
	if jsonOutput {
		// Best-effort post-promote re-read for --json, matching the classic
		// route's ignore-error re-read (labels included, like GetIssueInTx).
		if uwr, rerr := proxiedOpenReadUOW(ctx); rerr == nil {
			updated, _ = uowMolReader{uw: uwr}.GetIssue(ctx, fullID)
			uwr.Close(ctx)
		}
	}
	return printPromoteResult(fullID, updated)
}
