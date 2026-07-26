package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Claim-family verify-after-write (bd-zccb9, from wyvern incident wy-ejph3).
//
// Under a degraded sql-server the write's exit status is not truth in either
// direction: a claim can report success while the server-side transaction dies
// with the abandoned connection and rolls back (wy-ejph3: the lost claim cost a
// duplicate full implementation), and the inverse — an error printed with the
// write actually applied — is on record from wy-x543k. Fleet wrappers solved
// this with verify-by-re-read (wyvern's wh-take.sh); this file folds that
// protocol into bd itself for the coordination-critical writes: claim, ready
// claim, and unclaim. Ordinary field updates are out of scope — a lost label
// edit is an annoyance, a phantom claim is a duplicated implementation.
//
// The protocol: run the write, then resolve its outcome against the database
// instead of trusting the exit status.
//
//   - reported success        -> verify; a mismatch fails LOUDLY (the caller
//     must know it does not hold the claim)
//   - ambiguous commit loss   -> (errCommitPhase, surfaced by withRetryTx as
//     indeterminate) verify; applied -> success, verified rolled back -> replay
//     the write once (safe: nothing landed)
//   - any other error         -> honest failure (CAS lost, not-claimable, or
//     pre-commit errors withRetryTx already retried); no verify needed
//
// Wisps are exempt: they are ephemeral, never leased, and not reclaimable
// coordination state.

// claimPostcondition describes the row state a claim-family write must leave
// behind to count as applied, plus the words to use when it didn't.
type claimPostcondition struct {
	op   string // "claim" | "unclaim", for messages and metrics
	want func(assignee string, status types.Status) bool
	desc string // expected state, human-readable, for mismatch messages
}

func claimedBy(actor string) claimPostcondition {
	return claimPostcondition{
		op: "claim",
		want: func(assignee string, status types.Status) bool {
			return assignee == actor && status == types.StatusInProgress
		},
		desc: fmt.Sprintf("assignee=%q status=%q", actor, types.StatusInProgress),
	}
}

func unclaimed() claimPostcondition {
	return claimPostcondition{
		op: "unclaim",
		want: func(assignee string, status types.Status) bool {
			return assignee == "" && status == types.StatusOpen
		},
		desc: fmt.Sprintf("assignee=%q status=%q", "", types.StatusOpen),
	}
}

// guardedUpdatePostcondition derives the postcondition for a guarded update
// (bd-wsqvw: UpdateIssueOptions.ExpectedAssignee/ExpectedStatus) that writes
// the coordination fields. ok is false — no verification — when the update
// carries no guard, or when it guards but writes neither assignee nor status
// (a guarded edit of ordinary fields is not claim-family: a lost notes edit is
// an annoyance, not a phantom claim). The postcondition checks only the
// coordination fields the update actually sets, since the others may be
// legitimately touched by concurrent writers.
//
// Attribution narrowness (lion review, PR #5008): because only the SET
// coordination fields are checked, a commit-phase loss whose transaction
// verifiably rolled back can still verify as "applied" when a racing actor
// landed the SAME target values inside the verify window. The coordination
// state is then correct but it is the racer's write, not ours: any ordinary
// fields riding the same update (title, notes, …) and our event row are
// silently gone despite the reported success. Mitigation is usage-side, not
// machinery: keep guarded coordination writes single-purpose (assignee/status
// only), which is how the wheelhouse park/restore consumers use them.
func guardedUpdatePostcondition(opts storage.UpdateIssueOptions, updates map[string]interface{}) (claimPostcondition, bool) {
	if opts.ExpectedAssignee == nil && opts.ExpectedStatus == nil {
		return claimPostcondition{}, false
	}
	newAssignee, setsAssignee := updates["assignee"].(string)
	newStatus, setsStatus := updates["status"].(string)
	if !setsAssignee && !setsStatus {
		return claimPostcondition{}, false
	}
	var desc string
	switch {
	case setsAssignee && setsStatus:
		desc = fmt.Sprintf("assignee=%q status=%q", newAssignee, newStatus)
	case setsAssignee:
		desc = fmt.Sprintf("assignee=%q", newAssignee)
	default:
		desc = fmt.Sprintf("status=%q", newStatus)
	}
	return claimPostcondition{
		op: "guarded-update",
		want: func(assignee string, status types.Status) bool {
			if setsAssignee && assignee != newAssignee {
				return false
			}
			if setsStatus && status != types.Status(newStatus) {
				return false
			}
			return true
		},
		desc: desc,
	}, true
}

// readClaimState re-reads an issue's assignee and status on a fresh
// transaction. withReadTx retries transient connection errors itself, so a
// failure here means the server is genuinely unreachable, not a blip.
func (s *DoltStore) readClaimState(ctx context.Context, id string) (string, types.Status, error) {
	var assignee sql.NullString
	var status types.Status
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT assignee, status FROM issues WHERE id = ?`, id,
		).Scan(&assignee, &status)
	})
	if err != nil {
		return "", "", err
	}
	if !assignee.Valid {
		return "", status, nil
	}
	return assignee.String, status, nil
}

// verifiedClaimWrite runs write and resolves its outcome against the database
// state per the protocol above. write must be safe to run twice when its first
// run verifiably did not land (all claim-family writes are: they are CAS
// updates, idempotent for the winning actor).
//
// A verify that contradicts a reported success can in principle also be a
// legitimate concurrent mutation (a forced unclaim landing within the
// verification window). That reads as a lost write and fails loudly too —
// acceptable: the caller must re-establish its view either way.
func (s *DoltStore) verifiedClaimWrite(ctx context.Context, id string, post claimPostcondition, write func() error) error {
	if !s.serverMode || s.isActiveWisp(ctx, id) {
		return write()
	}
	const maxReplays = 1
	for attempt := 0; ; attempt++ {
		err := write()
		if err != nil && !errors.Is(err, errCommitPhase) {
			return err
		}
		assignee, status, verr := s.readClaimState(ctx, id)
		if verr != nil {
			if err != nil {
				return err // the honest indeterminate error from withRetryTx
			}
			return fmt.Errorf("%s of %s reported success but could not be verified (server degraded?): %w — re-read the issue before trusting the %s",
				post.op, id, verr, post.op)
		}
		if post.want(assignee, status) {
			if err != nil {
				doltMetrics.claimVerifyRecovered.Add(ctx, 1, metric.WithAttributes(
					attribute.String("op", post.op), attribute.String("outcome", "applied")))
			}
			return nil
		}
		if err != nil {
			if attempt < maxReplays {
				// Verified rolled back: nothing landed, so one replay is safe.
				doltMetrics.claimVerifyRecovered.Add(ctx, 1, metric.WithAttributes(
					attribute.String("op", post.op), attribute.String("outcome", "replayed")))
				continue
			}
			return fmt.Errorf("%s of %s did not land (connection lost during commit; rollback verified by re-read): %w",
				post.op, id, err)
		}
		doltMetrics.claimVerifyLost.Add(ctx, 1, metric.WithAttributes(
			attribute.String("op", post.op)))
		return fmt.Errorf("%s of %s reported success but did not land (found assignee=%q status=%q, want %s) — server likely degraded; treat the %s as NOT applied",
			post.op, id, assignee, status, post.desc, post.op)
	}
}
