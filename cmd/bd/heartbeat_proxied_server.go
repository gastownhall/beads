package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/workapi"
)

// heartbeatProxiedOutcome carries what the render layer needs out of the
// transaction: the fully-resolved ID and the title for the feedback line.
type heartbeatProxiedOutcome struct {
	id    string
	title string
}

// runHeartbeatProxiedServer routes bd heartbeat through the proxied-server
// plane. The lease write is EPHEMERAL (the dolt_ignored leases table,
// bd-lrgn1): the transaction commits via uow.RunTxEphemeral's SQL-only form,
// so a heartbeat mints exactly ZERO Dolt commits per invocation — the same
// commit discipline as the classic DoltStore.HeartbeatIssue, and deliberately
// nothing here sets commandDidWrite or creates a Dolt commit. The exit code
// is the worker contract (workers call this every ~90s and only check rc):
// 0 = lease refreshed; nonzero = the lease is gone (wrong owner, not
// in_progress, closed, reclaimed) and the worker should stop.
func runHeartbeatProxiedServer(ctx context.Context, id string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxEphemeral(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (heartbeatProxiedOutcome, error) {
		issue, _, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
		if errors.Is(rerr, storage.ErrNotFound) {
			return heartbeatProxiedOutcome{}, fmt.Errorf("issue %s not found", id)
		}
		if rerr != nil {
			return heartbeatProxiedOutcome{}, fmt.Errorf("resolving %s: %w", id, rerr)
		}
		// Wisps resolve here too and are refused below: the repo verb
		// classifies them ErrNotClaimable ("is ephemeral"), same as classic.
		if herr := uw.IssueUseCase().Heartbeat(ctx, issue.ID, actor); herr != nil {
			return heartbeatProxiedOutcome{}, fmt.Errorf("heartbeat %s: %w", issue.ID, herr)
		}
		return heartbeatProxiedOutcome{id: issue.ID, title: issue.Title}, nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	SetLastTouchedID(res.id)
	return renderHeartbeatSuccess(res.id, res.title)
}
