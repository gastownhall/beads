package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// runHeartbeatProxied is bd heartbeat on the proxied-server plane: the id is
// resolved exactly, on either plane, in a read unit of work of its own, and
// the refresh goes through the provider's OWN Heartbeater accessor (the
// capability the uow leg commits with RunTxScoped: a refresh names no tables
// and takes its SQL-only form, so it mints exactly ZERO Dolt commits,
// bd-lrgn1; a re-arm mints at most one, none when a concurrent writer's Dolt
// commit already took its row). Nothing here sets commandDidWrite.
// The exit code is the worker contract: 0 = lease refreshed; nonzero = the
// lease is gone and the worker should stop.
func runHeartbeatProxied(ctx context.Context, id string) error {
	issue, err := proxiedHeartbeatTarget(ctx, id)
	if err != nil {
		return err
	}
	heartbeater, err := proxiedHeartbeater()
	if err != nil {
		return HandleErrorRespectJSON("heartbeat %s: %v", issue.ID, err)
	}
	if _, err := heartbeater.Heartbeat(ctx, issueops.HeartbeatRequest{
		Actor:   currentActor(),
		IssueID: issue.ID,
	}); err != nil {
		return HandleErrorRespectJSON("heartbeat %s: %v", issue.ID, err)
	}
	SetLastTouchedID(issue.ID)
	return renderHeartbeatSuccess(issue.ID, issue.Title)
}

// proxiedHeartbeatTarget reads the row a proxied heartbeat names, on either
// plane (a wisp resolves here and the role refuses it as ephemeral), for its
// canonical id and the title the feedback line prints.
func proxiedHeartbeatTarget(ctx context.Context, id string) (*types.Issue, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return nil, err
	}
	defer uw.Close(ctx)
	issue, _, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, HandleErrorRespectJSON("issue %s not found", id)
	}
	if err != nil {
		return nil, HandleErrorRespectJSON("resolving %s: %v", id, err)
	}
	return issue, nil
}

// proxiedHeartbeater takes the lease refresh through the proxied-server
// provider's own capability accessor, proxiedLeaseReclaimer's two-step and for
// its reason: the accessor is where each layer is added.
func proxiedHeartbeater() (issueops.Heartbeater, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.HeartbeaterSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the lease-refresh surface", uowProvider)
	}
	return src.Heartbeater()
}
