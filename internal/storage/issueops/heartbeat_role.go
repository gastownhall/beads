package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateHeartbeatRequest applies the request rules every Heartbeater
// implementation shares. It is the ONE definition of a valid heartbeat: the
// library legs run it before they open a transaction, and the shared body runs
// it again, so a leg that skipped it still answers the same contract.
//
// THE ACTOR IS NOT TRIMMED, Releaser.Actor's rule: it is the fence's subject
// and is compared with the lease row's holder as sent. Only a blank one is
// refused, and one longer than the lease row's holder column can hold.
func ValidateHeartbeatRequest(request publicops.HeartbeatRequest) error {
	if strings.TrimSpace(request.Actor) == "" {
		return heartbeatFieldError(publicops.HeartbeatFieldActor, "heartbeat actor is required")
	}
	if err := types.CheckFieldLen("actor", request.Actor); err != nil {
		return heartbeatFieldError(publicops.HeartbeatFieldActor, err.Error())
	}
	if request.IssueID == "" {
		return heartbeatFieldError(publicops.HeartbeatFieldIssueID, "heartbeat issue id is required")
	}
	return nil
}

func heartbeatFieldError(field, detail string) error {
	return &publicops.HeartbeatFieldError{Field: field, Detail: storage.ErrValidation.Error() + ": " + detail}
}

// ExecuteHeartbeatInTx refreshes the lease request.Actor holds on
// request.IssueID, in tx. It is the body behind the Heartbeater accessor on
// ALL THREE library legs: dolt, embedded dolt and the unit-of-work provider.
//
// It is RenewLeaseInTx — the native heartbeat, SQL text unchanged — with the
// role's request rules in front and its typed refusals behind:
//
//   - a wisp is refused before anything is written, as every native leg
//     refuses it ("is ephemeral"): wisps are never leased;
//   - an id that names no row on either plane is storage.ErrNotFound, where
//     the native slow path answered ErrNotClaimable;
//   - a foreign holder and a missing live claim are *LeaseConflictError,
//     classified from the issue row the slow path already read.
//
// A REFUSAL MAY WRITE ONE THING: it deletes the caller's own lease row when
// that row survives on an issue the caller no longer holds a live claim on (an
// orphan; see RenewLeaseInTx). The legs therefore COMMIT the transaction of a
// refusal HeartbeatRefusalCommits recognizes and report the refusal after the
// commit, so the cleanup is not rolled back with it.
//
// WHAT A REFRESH MUST NEVER DO is everything RenewLeaseInTx's own doc rules
// out, and the conformance contract asserts each one: remint row_lock, record
// a version (current_revision, issue_versions), stamp updated_at, write an
// event or a journal entry. The legs add the other two: no version-control
// commit and no hook. A RE-ARM (HeartbeatResult.Rearmed) does every one of
// them, because it is recorded as a mutation: the legs commit it once, as
// LeaseRearmVersionCommit names, and the hook decorator fires on_update.
func ExecuteHeartbeatInTx(ctx context.Context, tx DBTX, request publicops.HeartbeatRequest) (publicops.HeartbeatResult, error) {
	if err := ValidateHeartbeatRequest(request); err != nil {
		return publicops.HeartbeatResult{}, err
	}
	id := request.IssueID

	var wispStatus string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ? LIMIT 1", id).Scan(&wispStatus); err == nil {
		return publicops.HeartbeatResult{}, &publicops.LeaseConflictError{
			IssueID:   id,
			Status:    types.Status(wispStatus),
			Ephemeral: true,
			Err:       fmt.Errorf("%w: %s is ephemeral", publicops.ErrLeaseNotHeld, id),
		}
	}

	renewal, err := RenewLeaseInTx(ctx, tx, id, request.Actor)
	if err != nil {
		var refusal *LeaseRenewalRefusal
		if !errors.As(err, &refusal) {
			return publicops.HeartbeatResult{}, err
		}
		return publicops.HeartbeatResult{}, classifyLeaseRenewalRefusal(refusal, request.Actor)
	}
	return publicops.HeartbeatResult{
		IssueID:        id,
		Holder:         request.Actor,
		HeartbeatAt:    renewal.HeartbeatAt,
		LeaseExpiresAt: renewal.ExpiresAt,
		Rearmed:        renewal.Rearmed,
	}, nil
}

// classifyLeaseRenewalRefusal types a native heartbeat refusal for the role.
func classifyLeaseRenewalRefusal(refusal *LeaseRenewalRefusal, actor string) error {
	id := refusal.IssueID
	if !refusal.Found {
		if errors.Is(refusal.ReadErr, sql.ErrNoRows) {
			return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
		}
		return fmt.Errorf("heartbeat %s: read issue: %w", id, refusal.ReadErr)
	}
	status := types.Status(refusal.Status)
	if refusal.Wisp {
		return &publicops.LeaseConflictError{
			IssueID: id, Status: status, Ephemeral: true,
			Err: fmt.Errorf("%w: %s is ephemeral", publicops.ErrLeaseNotHeld, id),
		}
	}
	if refusal.Assignee != "" && !actorMatches(refusal.Assignee, actor) {
		return &publicops.LeaseConflictError{
			IssueID: id, Holder: refusal.Assignee, Status: status, Err: refusal.Err,
		}
	}
	return &publicops.LeaseConflictError{
		IssueID: id, Status: status,
		Err: fmt.Errorf("%w: %s status %s", publicops.ErrLeaseNotHeld, id, refusal.Status),
	}
}

// HeartbeatRefusalCommits reports whether err is a heartbeat refusal whose
// transaction a leg must COMMIT rather than roll back: a typed
// *LeaseConflictError, the one refusal that can carry a write (the orphaned
// lease row ExecuteHeartbeatInTx deletes). Committing a refusal that wrote
// nothing is a no-op. Every other error rolls back as usual.
func HeartbeatRefusalCommits(err error) bool {
	var conflict *publicops.LeaseConflictError
	return errors.As(err, &conflict)
}
