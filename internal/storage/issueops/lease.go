package issueops

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// DefaultLeaseTTL is how long a fresh claim stays valid without a heartbeat.
// A worker is expected to call HeartbeatIssueInTx well within this window
// (heartbeat cadence ≫ claim cadence; see the commit-bloat note on bd heartbeat)
// so a live claim's lease_expires_at always sits in the future. A worker that
// dies stops heartbeating, its lease_expires_at goes stale, and bd reclaim
// reverts the issue to ready. Tunable per-claim via WithLeaseTTL on the
// context, falling back to this default.
const DefaultLeaseTTL = 5 * time.Minute

// leaseTTLContextKey overrides DefaultLeaseTTL for a single claim. Used by tests
// (short TTLs) and callers that know their work cadence; unset in normal use.
type leaseTTLContextKey struct{}

// WithLeaseTTL returns a context whose claims use ttl instead of DefaultLeaseTTL.
func WithLeaseTTL(ctx context.Context, ttl time.Duration) context.Context {
	return context.WithValue(ctx, leaseTTLContextKey{}, ttl)
}

// leaseTTL resolves the lease TTL for the current claim/heartbeat.
func leaseTTL(ctx context.Context) time.Duration {
	if ttl, ok := ctx.Value(leaseTTLContextKey{}).(time.Duration); ok && ttl > 0 {
		return ttl
	}
	return DefaultLeaseTTL
}

// freshRowLock returns a random non-zero int64 for the row_lock cell.
//
// row_lock is the keystone of dead-worker recovery on Dolt. Dolt has no real
// row locking and merges concurrent commits cell-by-cell, so two transactions
// that touch DIFFERENT cells of the same issue row (a heartbeat writing
// heartbeat_at, a close writing status) merge silently instead of conflicting —
// which would let a reclaim quietly revert an issue the owner just closed. By
// having every status/ownership/lease-mutating path rewrite this one shared cell
// to a fresh random value, those writers always collide on row_lock, surfacing
// the 1213/1205 serialization conflict that withRetryTx replays. The value's
// only job is to differ from whatever a concurrent writer wrote, so any source
// of entropy works; we use crypto/rand to avoid seeding concerns. Never 0 (the
// column default) so a freshly-claimed row is always distinguishable from a
// never-touched one.
//
// INVARIANT: any path that mutates status, assignee, started_at, or the lease
// columns on an in_progress issue MUST rewrite row_lock — that is the set the
// reclaim/heartbeat races care about (claim, close, updateIssueInTx, heartbeat,
// reclaim all do). Paths that touch only orthogonal cells (is_blocked,
// compaction_level, dependency metadata, rename, or reopen — which acts on
// closed rows) are safe to merge with a reclaim and intentionally do NOT rewrite
// it. Adding a new path that sets status/assignee/lease outside updateIssueInTx
// without rewriting row_lock would silently reintroduce the zombie-merge bug.
func freshRowLock() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic and ~never happens; fall back to a
		// timestamp so the row_lock still changes rather than wedging the write.
		return time.Now().UnixNano() | 1
	}
	v := int64(binary.LittleEndian.Uint64(b[:]))
	if v == 0 {
		v = 1
	}
	return v
}

// leaseSetClause returns the SET-clause fragment and args that stamp a fresh
// lease onto a row being claimed or heartbeated: a future expiry, a now
// heartbeat, and a fresh row_lock. Append to an existing UPDATE's SET list.
func leaseSetClause(now time.Time, ttl time.Duration) (string, []interface{}) {
	return "lease_expires_at = ?, heartbeat_at = ?, row_lock = ?",
		[]interface{}{now.Add(ttl), now, freshRowLock()}
}

// HeartbeatIssueInTx proves the lease owner is still alive: it pushes
// lease_expires_at forward by the TTL, stamps heartbeat_at = now, and rewrites
// row_lock so the heartbeat conflicts with any concurrent reclaim/close on the
// same row (see freshRowLock). Only the current owner of an in_progress issue
// may heartbeat — a heartbeat from anyone else, or on an issue that is no longer
// in_progress (already closed or already reclaimed), affects no rows and returns
// storage.ErrNotClaimable so the caller learns its lease is gone.
//
// Routes to the correct table (issues/wisps). The caller owns Dolt versioning.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func HeartbeatIssueInTx(ctx context.Context, tx DBTX, id, actor string) error {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, _, _ := WispTableRouting(isWisp)

	now := time.Now().UTC()
	leaseClause, leaseArgs := leaseSetClause(now, leaseTTL(ctx))

	// Stamp updated_at = now on the heartbeat. On Dolt/MySQL the issues/wisps
	// ON UPDATE CURRENT_TIMESTAMP trigger bumps updated_at on every heartbeat;
	// Postgres and SQLite have no such trigger, so without an explicit stamp an
	// actively-heartbeated issue keeps a stale updated_at and bd stale (which
	// filters in_progress rows on updated_at < cutoff) diverges from the Dolt
	// oracle. Claim already stamps updated_at explicitly, so this is heartbeat-only.
	args := append([]interface{}{}, leaseArgs...)
	args = append(args, now, id, actor)
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET %s, updated_at = ?
		WHERE id = ? AND status = 'in_progress' AND assignee = ?
	`, issueTable, leaseClause), args...)
	if err != nil {
		return fmt.Errorf("failed to heartbeat issue: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		// Disambiguate for the caller: gone (closed/reopened/reclaimed),
		// not-found, or owned by someone else.
		var assignee, status string
		qerr := tx.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COALESCE(assignee, ''), status FROM %s WHERE id = ?", issueTable), id,
		).Scan(&assignee, &status)
		if qerr != nil {
			return fmt.Errorf("%w: %s", storage.ErrNotClaimable, id)
		}
		if assignee != "" && assignee != actor {
			return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, assignee)
		}
		return fmt.Errorf("%w: %s status %s", storage.ErrNotClaimable, id, status)
	}
	return nil
}

// ReclaimExpiredLeasesInTx reverts in_progress issues whose lease has gone stale
// back to ready: status → open, assignee cleared, started_at cleared, and a
// fresh row_lock so the reclaim conflicts with a racing heartbeat/close on the
// same row (see freshRowLock). An issue is stale when its lease_expires_at is
// non-null and strictly before cutoff. Callers pass cutoff = now - graceWindow
// (the supervisor uses graceWindow = 2×TTL) so only leases that expired a safe
// margin ago — i.e. workers that are almost certainly dead — are reclaimed.
//
// Reclaim only ever touches the permanent issues table: wisps are ephemeral and
// are never leased work. Returns the issues it reverted (id + the owner it took
// the lease from) so the caller can log/emit recovery events. The caller owns
// Dolt versioning.
func ReclaimExpiredLeasesInTx(ctx context.Context, tx DBTX, cutoff time.Time, actor string) ([]types.ReclaimedLease, error) {
	// Snapshot the stale set first so we can report exactly which issues we
	// reverted and record per-issue recovery events. The UPDATE below repeats
	// the predicate, so an issue that a concurrent heartbeat rescued between the
	// SELECT and the UPDATE is simply skipped (0 rows) — it never appears as
	// reclaimed.
	rows, err := tx.QueryContext(ctx, `
		SELECT id, COALESCE(assignee, '') FROM issues
		WHERE status = 'in_progress'
		  AND lease_expires_at IS NOT NULL
		  AND lease_expires_at < ?
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("scan for stale leases: %w", err)
	}
	var stale []types.ReclaimedLease
	for rows.Next() {
		var r types.ReclaimedLease
		if err := rows.Scan(&r.ID, &r.PreviousOwner); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stale lease row: %w", err)
		}
		stale = append(stale, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate stale leases: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close stale lease rows: %w", err)
	}
	if len(stale) == 0 {
		return nil, nil
	}

	var reclaimed []types.ReclaimedLease
	for _, r := range stale {
		// Re-check the predicate inside the UPDATE so a heartbeat that landed
		// after the snapshot (pushing lease_expires_at back into the future, or
		// the row already closed) cannot be clobbered. row_lock makes the racing
		// writer conflict; this WHERE makes a winning racer's rescue stick.
		res, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET status = 'open', assignee = NULL, started_at = NULL,
			    lease_expires_at = NULL, heartbeat_at = NULL,
			    updated_at = ?, row_lock = ?
			WHERE id = ? AND status = 'in_progress'
			  AND lease_expires_at IS NOT NULL AND lease_expires_at < ?
		`, time.Now().UTC(), freshRowLock(), r.ID, cutoff)
		if err != nil {
			return nil, fmt.Errorf("reclaim %s: %w", r.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("reclaim %s rows affected: %w", r.ID, err)
		}
		if n == 0 {
			continue // rescued by a concurrent heartbeat/close — leave it be
		}
		if err := RecordFullEventInTable(ctx, tx, "events", r.ID, types.EventLeaseReclaimed, actor,
			r.PreviousOwner, ""); err != nil {
			return nil, fmt.Errorf("record reclaim event for %s: %w", r.ID, err)
		}
		reclaimed = append(reclaimed, r)
	}
	return reclaimed, nil
}
