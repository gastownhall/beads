package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ClaimResult holds the result of a ClaimIssueInTx call.
type ClaimResult struct {
	OldIssue *types.Issue
	IsWisp   bool
}

// ClaimIssueInTx atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// is currently open and unassigned, already assigned to the same actor, or
// assigned to a pool alias listed in the claim.pools config (see
// ClaimPoolAliasesInTx).
// Returns storage.ErrAlreadyClaimed if already claimed by a different user.
// Idempotent: re-claiming an in_progress issue by the same actor is a no-op
// success (supports agent retry workflows).
// Routes to the correct table (issues/wisps) automatically.
// The caller is responsible for Dolt versioning (DOLT_ADD/COMMIT) if needed.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func ClaimIssueInTx(ctx context.Context, tx DBTX, id string, actor string) (*ClaimResult, error) {
	// The CAS below writes assignee = actor. actor is user-settable (--actor /
	// BEADS_ACTOR), so bound it against the VARCHAR(255) assignee column up front
	// and return a typed ErrFieldTooLong rather than a raw backend error.
	if err := types.CheckFieldLen("actor", actor); err != nil {
		return nil, err
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	// Read old issue inside the transaction for event recording.
	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue for claim: %w", err)
	}

	now := time.Now().UTC()

	// Rewrite row_lock with the claim (see lease.go): a concurrent reclaim or
	// close on the same row is forced to conflict rather than silently
	// cell-merge. The lease itself is granted separately below, in the
	// ephemeral leases table — claims commit (status/assignee are
	// history-worthy) but lease grants and heartbeats do not (bd-lrgn1).
	rowLockClause, rowLockArgs := RowLockClause()

	// An issue is claimable from "open" plus any configured custom status whose
	// category is "active" (e.g. a draft->ready->in_progress lifecycle where
	// "ready" should be claimable). WIP/done/frozen customs are excluded so the
	// anti-steal protection from GH-3570 is preserved.
	claimableStatuses, err := ClaimableSourceStatusesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve claimable statuses: %w", err)
	}
	statusPlaceholders, statusArgs := buildSQLInClause(claimableStatuses)

	// Pool-aware claim (bd-bguz6): a dispatcher may pre-assign issues to a
	// pool pseudo-assignee (e.g. "fable-crew"). Aliases listed in the
	// claim.pools config are claimable by any actor through the same CAS;
	// issues assigned to a real actor keep their anti-steal protection.
	pools, err := ClaimPoolAliasesInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve claim pools: %w", err)
	}

	// Claimability of the assignee slot, judged in Go against oldIssue (the
	// pre-image read above, inside this same transaction) rather than as a
	// spelling-sensitive SQL predicate (ga-v2k49, same shape as ga-5ksp5's
	// unclaim.go fix): empty/unassigned, already this actor — including an
	// idempotent re-claim spelled under a different layer's separator
	// convention (ga-wzl83) — or assigned to a claim-pool alias. That last
	// membership test is deliberately exact-string, unlike actorMatches right
	// beside it: a pool alias (e.g. "fable-crew") is a literal claim.pools
	// config value, not a Gas Town identity that gets respelled per layer, so
	// there is no cross-spelling variant to reconcile — canonicalizing it
	// could only blur two administratively-distinct pool names into one.
	assigneeOK := oldIssue.Assignee == "" || actorMatches(oldIssue.Assignee, actor) || slices.Contains(pools, oldIssue.Assignee)

	// Conditional UPDATE: only attempted while the issue was still claimable
	// as of oldIssue, and even then CASed on row_lock rather than re-checking
	// assignee/status in SQL — row_lock is rewritten by every path that
	// mutates status/assignee/started_at (see the freshRowLock invariant in
	// lease.go), so requiring it to still equal oldIssue.RowVersion detects a
	// race (reassigned, unclaimed, or transitioned between our read and this
	// write) exactly as precisely as the old predicate did, without
	// embedding a spelling-sensitive string comparison in SQL. When
	// assigneeOK is false, skip the UPDATE entirely (there is nothing this
	// actor could win) and fall straight into the same rowsAffected==0
	// disambiguation path below — matches today's behavior without a wasted
	// write attempt. Also set started_at on first transition to in_progress
	// (GH#2796); preserve any existing value so re-claims don't overwrite the
	// original start time.
	var rowsAffected int64
	if assigneeOK {
		var result sql.Result
		if oldIssue.StartedAt == nil {
			args := append([]interface{}{actor, now, now}, rowLockArgs...)
			args = append(args, id, oldIssue.RowVersion)
			args = append(args, statusArgs...)
			result, err = tx.ExecContext(ctx, fmt.Sprintf(`
				UPDATE %s
				SET assignee = ?, status = 'in_progress', updated_at = ?, started_at = ?, %s
				WHERE id = ? AND row_lock = ? AND status IN (%s)
			`, issueTable, rowLockClause, statusPlaceholders), args...)
		} else {
			args := append([]interface{}{actor, now}, rowLockArgs...)
			args = append(args, id, oldIssue.RowVersion)
			args = append(args, statusArgs...)
			result, err = tx.ExecContext(ctx, fmt.Sprintf(`
				UPDATE %s
				SET assignee = ?, status = 'in_progress', updated_at = ?, %s
				WHERE id = ? AND row_lock = ? AND status IN (%s)
			`, issueTable, rowLockClause, statusPlaceholders), args...)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to claim issue: %w", err)
		}
		rowsAffected, err = result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("failed to get rows affected: %w", err)
		}
	}

	if rowsAffected == 0 {
		assignee, currentStatus, err := readClaimStateInTx(ctx, tx, issueTable, id)
		if err != nil {
			return nil, fmt.Errorf("failed to get current claim state: %w", err)
		}
		// Idempotent: if already claimed in_progress by the same actor —
		// including a spelling difference across layers (ga-wzl83) — treat as
		// success. This supports agent retry workflows where claim may be
		// called multiple times after transient failures (GH#8).
		if actorMatches(assignee, actor) && currentStatus == types.StatusInProgress {
			return &ClaimResult{OldIssue: oldIssue, IsWisp: isWisp}, nil
		}
		// The refusal carries the state that lost the CAS, read just above in
		// THIS transaction, so a caller learns who won without parsing the
		// message. The typed wrapper carries the fields; the PROSE is composed
		// here, because ClaimConflictError.Error() passes its wrapped refusal
		// through byte-for-byte — a bare sentinel would reach the caller as
		// "issue already claimed" with the holder dropped and
		// beads.ParseClaimConflict unable to recover it. The fragments are the
		// storage layer's exported ones, which is what keeps the parser and
		// this producer in step. The sentinel stays matchable through both
		// wraps, which errors.Is, ParseClaimConflict and the proxied batch
		// exit code all key on.
		refusal := fmt.Errorf("%w%s%s", storage.ErrNotClaimable, storage.NotClaimableStatusFragment, currentStatus)
		if assignee != "" && !actorMatches(assignee, actor) {
			switch {
			// A pool-assigned issue reaches here only when the CAS lost for a
			// non-assignee reason (status changed underneath us): report the
			// status rather than a misleading held-by-someone refusal. Checked
			// FIRST, so a pool alias never falls into the holder-steering copy.
			// Exact-string membership, same reason as assigneeOK's identical
			// term above: a pool alias is a literal config value, not a
			// respelled identity.
			case slices.Contains(pools, assignee):
				// refusal already names the status.
			case currentStatus == types.StatusOpen:
				// Do not name a release command here — not `bd unclaim`, not
				// `bd unclaim --force`. Refusal copy that names one gets
				// pattern-matched by batch agents into an unclaim+claim
				// steamroller of live claims (wy-yuclk). Point at the holder;
				// bd reclaim is safe to name because it only recovers claims
				// whose lease has already expired.
				//
				// This copy deliberately omits the parseable " by <assignee>"
				// tail, so ParseClaimConflict recovers the holder from the
				// typed field rather than the prose (bd-at6rc).
				refusal = fmt.Errorf("%w: already assigned to %q — coordinate with the holder; if their claim is abandoned (crashed agent), lease expiry will surface it for bd reclaim", storage.ErrAlreadyClaimed, assignee)
			default:
				refusal = fmt.Errorf("%w%s%s", storage.ErrAlreadyClaimed, storage.ClaimedByFragment, assignee)
			}
		}
		return nil, &publicops.ClaimConflictError{
			IssueID:  id,
			Assignee: assignee,
			Status:   currentStatus,
			Err:      refusal,
		}
	}

	// Grant the lease: what makes the claim recoverable — a worker that dies
	// stops heartbeating and bd reclaim later reverts the issue. Lease rows
	// live in the ephemeral leases table (no Dolt commit, node-local). Wisps
	// are never leased (they are ephemeral, not reclaimable work).
	if !isWisp {
		if err := UpsertLeaseInTx(ctx, tx, id, actor, now, leaseTTL(ctx)); err != nil {
			return nil, err
		}
	}

	// Record the claim event.
	oldData, _ := json.Marshal(oldIssue)
	newUpdates := map[string]interface{}{
		"assignee": actor,
		"status":   "in_progress",
	}
	newData, _ := json.Marshal(newUpdates)

	if err := RecordFullEventInTable(ctx, tx, eventTable, id, types.EventClaimed, actor, string(oldData), string(newData)); err != nil {
		return nil, fmt.Errorf("failed to record claim event: %w", err)
	}

	// A claim changes assignee and status, so it journals as an update. The
	// idempotent re-claim path returns above without writing and journals
	// nothing.
	if err := RecordEventInTx(ctx, tx, EventUpdate, id); err != nil {
		return nil, err
	}

	return &ClaimResult{OldIssue: oldIssue, IsWisp: isWisp}, nil
}

// readClaimStateInTx reads one row's coordination columns inside tx — the
// state a lost compare-and-set reports. Shared by the CAS itself and by the
// public claim role, so the two cannot disagree about what "the state that
// refused this claim" means.
//
//nolint:gosec // G201: issueTable comes from WispTableRouting (hardcoded constants)
func readClaimStateInTx(ctx context.Context, tx DBTX, issueTable, id string) (string, types.Status, error) {
	var assignee sql.NullString
	var status types.Status
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT assignee, status FROM %s WHERE id = ?`, issueTable), id).Scan(&assignee, &status); err != nil {
		return "", "", err
	}
	return assignee.String, status, nil
}

// ClaimReadyIssueInTx claims the first currently ready issue matching filter in
// the same transaction that computes readiness. It returns nil when no matching
// ready issue can be claimed.
func ClaimReadyIssueInTx(
	ctx context.Context,
	tx DBTX,
	filter types.WorkFilter,
	actor string,
) (*types.Issue, error) {
	claimFilter := filter
	claimFilter.Status = types.StatusOpen
	claimFilter.Unassigned = true
	claimFilter.Assignee = nil
	// Claim only ever delivers the one issue it successfully claims below —
	// the breaker's job is to bound delivered payloads, and a claim's
	// payload is always exactly one row regardless of how large the ready
	// pool it scanned was. So a rig-wide BEADS_MAX_ROWS/--max-rows cap
	// (sized for bulk list/ready reads) must not fire here, and the scan
	// itself must stay unbounded (Limit=0): the loop below walks
	// readyIssues in order and continues past any that are transiently
	// unclaimable (already claimed by a racing agent, etc), so bounding
	// the scan to Limit=MaxRows (e.g. BEADS_MAX_ROWS=1 → scan only the
	// single top-of-queue row) would make claim spuriously return "nothing
	// to claim" whenever that narrow window is unclaimable, even with
	// plenty of other ready work available. Clear the cap fields so
	// GetReadyWorkInTx never returns ErrTooManyRows either.
	//
	// This is parity with pre-PR main (which never bounded the claim scan)
	// and correctness-first: an unbounded scan preserves claim's existing
	// fairness/ordering guarantee across the whole ready set. A paged scan
	// that stays bounded while still walking past unclaimable rows is a
	// reasonable follow-up, but it's a genuine behavior change (not a
	// MaxRows-cap fix) and is deliberately deferred rather than folded in
	// here.
	claimFilter.Limit = 0
	claimFilter.MaxRows = 0
	claimFilter.MaxRowsSource = ""

	readyIssues, err := GetReadyWorkInTx(ctx, tx, claimFilter)
	if err != nil {
		return nil, err
	}
	for _, issue := range readyIssues {
		if _, err := ClaimIssueInTx(ctx, tx, issue.ID, actor); err != nil {
			if errors.Is(err, storage.ErrAlreadyClaimed) || errors.Is(err, storage.ErrNotClaimable) {
				continue
			}
			return nil, err
		}
		claimed, err := GetIssueInTx(ctx, tx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("get claimed issue: %w", err)
		}
		return claimed, nil
	}
	return nil, nil
}

// ClaimPoolAliasesInTx returns the pool pseudo-assignee aliases from the
// claim.pools config key (comma-separated, whitespace-trimmed). An issue
// assigned to one of these aliases is claimable by ANY actor through the
// normal claim CAS — the pattern where a dispatcher pre-assigns work to a
// group alias (e.g. "fable-crew") and members take items from the pool.
// Issues assigned to a real actor are unaffected. Missing/empty config (the
// default) disables pool-aware claiming entirely.
func ClaimPoolAliasesInTx(ctx context.Context, tx DBTX) ([]string, error) {
	raw, err := GetConfigInTx(ctx, tx, "claim.pools")
	if err != nil {
		return nil, err
	}
	return ParseClaimPools(raw), nil
}

// ParseClaimPools parses a raw claim.pools config value (comma-separated,
// whitespace-trimmed) into the pool alias list. Shared by the claim CAS
// (ClaimPoolAliasesInTx, and its domain/db dual) and the cmd-layer reassign
// fence (bd-98s5c), so the alias set can never drift between --claim and
// -a/--assignee — the two verbs must agree on which holders are pools.
func ParseClaimPools(raw string) []string {
	var pools []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			pools = append(pools, p)
		}
	}
	return pools
}

// ClaimableSourceStatusesInTx returns the set of statuses an issue may be
// claimed FROM: the built-in "open" status plus any configured custom status
// whose category is "active" (the same category that surfaces issues in
// bd ready). Custom statuses in the wip/done/frozen categories are intentionally
// excluded so claim retains its anti-steal protection (GH-3570) — an
// in_progress/blocked issue, or a custom alias for one, is never silently
// re-claimable. Unspecified-category customs are also excluded, matching their
// absence from bd ready.
func ClaimableSourceStatusesInTx(ctx context.Context, tx DBTX) ([]string, error) {
	statuses := []string{string(types.StatusOpen)}
	customs, err := ResolveCustomStatusesDetailedInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, s := range customs {
		if s.Category == types.CategoryActive {
			statuses = append(statuses, s.Name)
		}
	}
	return statuses, nil
}
