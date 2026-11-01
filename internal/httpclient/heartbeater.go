package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// httpHeartbeater is issueops.Heartbeater over heartbeatIssue (POST
// /v0/beads/issues/{id}:heartbeat), the lease refresh behind `bd heartbeat`.
//
// THE MAPPING IS TOTAL: the id rides in the path and the actor is the body's one
// member, so nothing here refuses on shape and the ledger carries no row for this
// operation. The answer carries no revision, deliberately: it reports the
// lease, not the issue. A refresh writes only the lease, so there is no token
// to stitch back; a re-arm remints it, which Rearmed reports so the caller
// re-reads the issue before a guarded write.
//
// THE REQUEST RULES ARE CHECKED BEFORE THE DIAL, in the order
// internal/storage/issueops.ValidateHeartbeatRequest applies them and with its
// *issueops.HeartbeatFieldError, for leasereclaimer.go's reason: the role's
// contract promises the typed field error, and a round trip would spend a write
// slot to be told what the request already said. The rules are restated rather
// than imported (that package is the storage engine's shared body);
// TestHeartbeatValidationMatchesTheSharedValidator pins the two together.
//
// AN OLDER SERVER IS REFUSED BEFORE THE DIAL. Store.dispatch preflights
// issues.heartbeat, so a server that does not advertise it answers
// *wire.CapabilityError naming the token (taxonomy case 2), never a fallback
// to a re-claim or a metadata stamp: a re-claim by the holder does not extend
// the lease, and a stamp would remint the revision the holder's guarded
// writes depend on.
//
// IT IS REPLAYABLE (wire.Request.Idempotent), the one write this client marks
// so. A repeat only moves the expiry later and writes nothing else (a repeat
// of a re-arm finds the lease row the first one granted and refreshes it), and
// a replay that lands after a reclaim fails the holder fence, which is the
// answer the worker needs anyway. So a 503 is retried within dispatch's bound
// rather than reported to a worker that heartbeats every ~90s and stops on any
// nonzero exit.
//
// THE COST: A REPLAY CAN UNDER-REPORT Rearmed. When a re-arm commits but its
// answer is lost (the wire resends after a stalled or reset attempt, dispatch
// after a 503), the replay finds the lease row the re-arm granted and answers
// a refresh, Rearmed false, though the revision token was reminted. The
// backstop is the guarded write itself: one composed on the old revision fails
// its revision check once and re-reads (see issueops.HeartbeatResult.Rearmed).
type httpHeartbeater struct{ store *Store }

var _ issueops.Heartbeater = (*httpHeartbeater)(nil)

// Heartbeat dials POST /v0/beads/issues/{id}:heartbeat.
//
// THE REFUSALS reach the caller typed as the role lists them: not_found is
// issueops.ErrNotFound, lease_not_held is *issueops.LeaseConflictError
// wrapping ErrLeaseNotHeld (the wire's code table builds it), and
// already_claimed — which the wire's table rebuilds as the CLAIM's conflict,
// the code's meaning on every other operation — is re-typed here as the
// heartbeat's *LeaseConflictError naming the holder (heartbeatError).
func (h *httpHeartbeater) Heartbeat(ctx context.Context, req issueops.HeartbeatRequest) (issueops.HeartbeatResult, error) {
	if err := validateHeartbeatRequest(req); err != nil {
		return issueops.HeartbeatResult{}, err
	}
	path, err := wire.IssueMethodPath(req.IssueID, wire.MethodHeartbeat)
	if err != nil {
		return issueops.HeartbeatResult{}, err
	}

	var out apigen.HeartbeatIssueResponse
	if err := h.store.dispatch(ctx, wire.Request{
		Op:         wire.OpHeartbeatIssue,
		Method:     http.MethodPost,
		Path:       path,
		Body:       apigen.HeartbeatIssueRequest{Actor: req.Actor},
		IssueID:    req.IssueID,
		Actor:      req.Actor,
		Idempotent: true,
	}, &out); err != nil {
		return issueops.HeartbeatResult{}, heartbeatError(err)
	}
	return issueops.HeartbeatResult{
		IssueID:        out.Id,
		Holder:         out.Holder,
		HeartbeatAt:    out.HeartbeatAt.UTC(),
		LeaseExpiresAt: out.LeaseExpiresAt.UTC(),
		Rearmed:        out.Rearmed,
	}, nil
}

// heartbeatError re-types the two refusals the shared problem table cannot
// spell as the heartbeat's.
//
//   - already_claimed arrives as *issueops.ClaimConflictError (the claim's
//     shape, from the same code). On this operation it is a foreign holder,
//     so it becomes *issueops.LeaseConflictError naming that holder and
//     wrapping ErrAlreadyClaimed, read in the native heartbeat's own words
//     ("issue already claimed by <holder>").
//   - invalid_argument on `actor` is the server's actor rule (the claim's,
//     which also refuses control characters) and becomes the role's
//     *HeartbeatFieldError on that field.
//
// Everything else — lease_not_held, not_found, a capability refusal, a
// transport failure — is already the role's answer and travels unchanged.
func heartbeatError(err error) error {
	var claim *issueops.ClaimConflictError
	if errors.As(err, &claim) && errors.Is(err, issueops.ErrAlreadyClaimed) {
		refusal := issueops.ErrAlreadyClaimed
		if claim.Assignee != "" {
			refusal = fmt.Errorf("%w by %s", issueops.ErrAlreadyClaimed, claim.Assignee)
		}
		return &issueops.LeaseConflictError{
			IssueID: claim.IssueID, Holder: claim.Assignee, Status: claim.Status, Err: refusal,
		}
	}
	var problem *wire.ProblemError
	if errors.As(err, &problem) && problem.Code == "invalid_argument" && problem.Param == "actor" {
		return &issueops.HeartbeatFieldError{Field: issueops.HeartbeatFieldActor, Detail: problem.Detail}
	}
	return err
}

// validateHeartbeatRequest restates the role's request rules, in its order:
// the actor present (blank once trimmed is absent), the actor within its
// column AS SENT (it is never trimmed: it is compared with the lease holder
// verbatim), then a non-empty id.
func validateHeartbeatRequest(req issueops.HeartbeatRequest) error {
	field := func(name, detail string) error {
		return &issueops.HeartbeatFieldError{Field: name, Detail: issueops.ErrValidation.Error() + ": " + detail}
	}
	if strings.TrimSpace(req.Actor) == "" {
		return field(issueops.HeartbeatFieldActor, "heartbeat actor is required")
	}
	if err := types.CheckFieldLen("actor", req.Actor); err != nil {
		return field(issueops.HeartbeatFieldActor, err.Error())
	}
	if req.IssueID == "" {
		return field(issueops.HeartbeatFieldIssueID, "heartbeat issue id is required")
	}
	return nil
}

// HeartbeatIssue is the legacy lease refresh, served over the role so an
// in-process library caller holding a storage.DoltStorage reaches the same
// operation `bd heartbeat` does, with the same refusals.
func (s *Store) HeartbeatIssue(ctx context.Context, id, actor string) error {
	role, err := s.Heartbeater()
	if err != nil {
		return err
	}
	_, err = role.Heartbeat(ctx, issueops.HeartbeatRequest{Actor: actor, IssueID: id})
	return err
}
