package dolt

import (
	"context"
	"database/sql"
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
//   - ambiguous commit loss   -> retain ErrCommitIndeterminate. Assignee and
//     status alone cannot prove the lease and actor-attributed event landed.
//   - any other error         -> honest failure (CAS lost, not-claimable, or
//     pre-commit errors withRetryTx already retried); no verify needed
//
// A claim the caller NAMES by id is exempt when the id is a wisp: wisps are
// ephemeral, never leased, and not reclaimable coordination state, so the
// verify round trip buys nothing. A READY claim is different — it does not know
// what it won until the write has run, and what it won may be a wisp, because
// the ready set follows the request's filter and IncludeEphemeral admits the
// wisp plane. That one verifies whatever it claimed, in the plane it wrote
// (see readReadyClaimState).

// claimPostcondition describes the row state a claim-family write must leave
// behind to count as applied, plus the words to use when it didn't.
type claimPostcondition struct {
	op   string // "claim" | "unclaim", for messages and metrics
	want func(assignee string, status types.Status) bool
	desc string // expected state, human-readable, for mismatch messages
}

func claimedBy(actor string) claimPostcondition {
	return claimedAs(actor, types.StatusInProgress)
}

// claimedAs is claimedBy for a claim whose request overrides the assignee or
// status the bare claim would have written.
func claimedAs(assignee string, status types.Status) claimPostcondition {
	return claimPostcondition{
		op: "claim",
		want: func(gotAssignee string, gotStatus types.Status) bool {
			return gotAssignee == assignee && gotStatus == status
		},
		desc: fmt.Sprintf("assignee=%q status=%q", assignee, status),
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
// carries no guard, when it guards but writes neither assignee nor status, or
// when ordinary fields ride alongside the coordination write. Verification
// reads only assignee/status, so it cannot prove that a mixed update (or its
// event) landed; such a write must retain ErrCommitIndeterminate instead of
// turning a matching coordination state into false success. For a
// coordination-only update, the postcondition checks every field requested by
// the caller while allowing other coordination fields to change concurrently.
func guardedUpdatePostcondition(opts storage.UpdateIssueOptions, updates map[string]interface{}) (claimPostcondition, bool) {
	if opts.ExpectedAssignee == nil && opts.ExpectedStatus == nil {
		return claimPostcondition{}, false
	}
	for field := range updates {
		if field != "assignee" && field != "status" {
			return claimPostcondition{}, false
		}
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
	return s.readClaimStateIn(ctx, "issues", id)
}

// readReadyClaimState re-reads a ready claim's winner in the plane the claim
// actually wrote. A ready claim can win an ephemeral row — the ready set is
// whatever the request's filter admits, and IncludeEphemeral admits the wisp
// plane — and issueops.ClaimIssueInTx routes the compare-and-set by plane
// (IsActiveWispInTx then WispTableRouting), so the compare-and-set lands on
// wisps. Verifying that against `issues` finds no row and reports a write that
// demonstrably succeeded as unverifiable (bd-yby99.4).
//
// A failed plane probe degrades to the issues plane, which is where a durable
// claim — the overwhelmingly common case — belongs anyway.
func (s *DoltStore) readReadyClaimState(ctx context.Context, id string) (string, types.Status, error) {
	if s.isActiveWisp(ctx, id) {
		return s.readClaimStateIn(ctx, "wisps", id)
	}
	return s.readClaimStateIn(ctx, "issues", id)
}

// readClaimStateIn is readClaimState against a named plane. Both planes carry
// the same two coordination columns, so the postcondition is the same either
// way; only the table differs.
//
//nolint:gosec // G201: table is one of the two plane names this file passes.
func (s *DoltStore) readClaimStateIn(ctx context.Context, table, id string) (string, types.Status, error) {
	var assignee sql.NullString
	var status types.Status
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT assignee, status FROM `+table+` WHERE id = ?`, id,
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
// state per the protocol above.
//
// A verify that contradicts a reported success can in principle also be a
// legitimate concurrent mutation (a forced unclaim landing within the
// verification window). That reads as a lost write and fails loudly too —
// acceptable: the caller must re-establish its view either way.
func (s *DoltStore) verifiedClaimWrite(ctx context.Context, id string, post claimPostcondition, write func() error) error {
	if !s.serverMode || s.isActiveWisp(ctx, id) {
		return write()
	}
	err := write()
	if err != nil {
		return err
	}
	assignee, status, verr := s.readClaimState(ctx, id)
	if verr != nil {
		return fmt.Errorf("%s of %s reported success but could not be verified (server degraded?): %w — re-read the issue before trusting the %s",
			post.op, id, verr, post.op)
	}
	if post.want(assignee, status) {
		return nil
	}
	doltMetrics.claimVerifyLost.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", post.op)))
	return fmt.Errorf("%s of %s reported success but did not land (found assignee=%q status=%q, want %s) — server likely degraded; treat the %s as NOT applied",
		post.op, id, assignee, status, post.desc, post.op)
}
