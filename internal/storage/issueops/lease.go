package issueops

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strings"
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

// explicitLeaseTTL reports whether the caller explicitly requested a lease
// for this claim (WithLeaseTTL) — the opt-in that stamps a lease even when
// automatic stamping is disarmed.
func explicitLeaseTTL(ctx context.Context) (time.Duration, bool) {
	ttl, ok := ctx.Value(leaseTTLContextKey{}).(time.Duration)
	return ttl, ok && ttl > 0
}

// LeaseAutoConfigKey is the store config key governing automatic lease
// stamping on claim. Default (unset/"on") preserves the shipped semantics:
// every claim stamps a DefaultLeaseTTL lease and a supervisor `bd reclaim`
// recovers dead workers. "off" disarms automatic stamping for deployments
// whose recovery authority lives elsewhere (an orchestrator with its own
// liveness evidence): claims carry NULL lease columns — invisible to
// reclaim — and only explicitly requested leases (WithLeaseTTL /
// --lease-ttl) are ever reclaimable. See `bd lease disarm`.
const LeaseAutoConfigKey = "lease.auto"

// autoLeaseEnabled reads the lease.auto store config inside the claim's
// transaction. Unset and unrecognized values default to on (upstream
// semantics unchanged); only an explicit off/false/0 (case-insensitive)
// disarms. A config-read failure is propagated, never guessed: lease.auto is
// a safety knob, and silently arming on a disarmed store would re-create the
// unrequested reclaim exposure disarming removes (while eating the
// serialization aborts withRetryTx exists to replay).
func autoLeaseEnabled(ctx context.Context, tx DBTX) (bool, error) {
	v, err := GetConfigInTx(ctx, tx, LeaseAutoConfigKey)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", LeaseAutoConfigKey, err)
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "false", "0":
		return false, nil
	}
	return true, nil
}

// ClaimLeaseClause renders the lease columns for a claim: a fresh lease when
// one was explicitly requested or automatic stamping is on, NULLs otherwise —
// an unleased claim also scrubs any stale lease left by a legacy release
// path, so a later reclaim can never key on leftovers. Both shapes rewrite
// row_lock (the fence-pairing and anti-cell-merge invariant).
func ClaimLeaseClause(ctx context.Context, tx DBTX, now time.Time) (string, []interface{}, error) {
	if ttl, explicit := explicitLeaseTTL(ctx); explicit {
		clause, args := leaseSetClause(now, ttl)
		return clause, args, nil
	}
	auto, err := autoLeaseEnabled(ctx, tx)
	if err != nil {
		return "", nil, err
	}
	if auto {
		clause, args := leaseSetClause(now, DefaultLeaseTTL)
		return clause, args, nil
	}
	return "lease_expires_at = NULL, heartbeat_at = NULL, row_lock = ?", []interface{}{freshRowLock()}, nil
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

// LeaseSetClause is the exported form of leaseSetClause, for the proxied-server
// (uow) claim path in internal/storage/domain/db, which builds its own claim
// UPDATE rather than calling ClaimIssueInTx. Keeping both paths on this one
// helper is what stops the proxied path from reintroducing the missing-lease /
// cell-merge bug the row_lock invariant guards against.
func LeaseSetClause(now time.Time, ttl time.Duration) (string, []interface{}) {
	return leaseSetClause(now, ttl)
}

// LeaseTTL is the exported form of leaseTTL: it resolves the lease TTL for the
// current claim from the context (WithLeaseTTL) or falls back to
// DefaultLeaseTTL.
func LeaseTTL(ctx context.Context) time.Duration {
	return leaseTTL(ctx)
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
	// The lease_expires_at IS NOT NULL predicate keeps heartbeat a RENEWAL:
	// it must never ARM a lease on a deliberately unleased claim (lease.auto
	// off) — that would silently re-create the unrequested reclaim exposure
	// disarming exists to remove.
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET %s, updated_at = ?
		WHERE id = ? AND status = 'in_progress' AND assignee = ?
		  AND lease_expires_at IS NOT NULL
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
		// not-found, owned by someone else, or owned-but-unleased.
		var assignee, status string
		var leaseExpires sql.NullTime
		qerr := tx.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COALESCE(assignee, ''), status, lease_expires_at FROM %s WHERE id = ?", issueTable), id,
		).Scan(&assignee, &status, &leaseExpires)
		if qerr != nil {
			return fmt.Errorf("%w: %s", storage.ErrNotClaimable, id)
		}
		if assignee != "" && assignee != actor {
			return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, assignee)
		}
		if assignee == actor && status == string(types.StatusInProgress) && !leaseExpires.Valid {
			// Owned, in progress, no lease. Under the shipped default
			// (lease.auto on) this is a legacy row from before the lease
			// stack — the shipped heartbeat ARMED it, converging it into the
			// lease regime, and that behavior is preserved. Only a disarmed
			// store rejects: heartbeat there is strictly a renewal and must
			// never arm a lease as a side effect.
			auto, aerr := autoLeaseEnabled(ctx, tx)
			if aerr != nil {
				return aerr
			}
			if !auto {
				return fmt.Errorf("%w: %s", storage.ErrUnleased, id)
			}
			armArgs := append([]interface{}{}, leaseArgs...)
			armArgs = append(armArgs, id, actor)
			res, err := tx.ExecContext(ctx, fmt.Sprintf(`
				UPDATE %s SET %s
				WHERE id = ? AND status = 'in_progress' AND assignee = ?
			`, issueTable, leaseClause), armArgs...)
			if err != nil {
				return fmt.Errorf("failed to arm legacy lease: %w", err)
			}
			if n, err := res.RowsAffected(); err == nil && n > 0 {
				return nil
			}
			return fmt.Errorf("%w: %s status %s", storage.ErrNotClaimable, id, status)
		}
		return fmt.Errorf("%w: %s status %s", storage.ErrNotClaimable, id, status)
	}
	return nil
}

// DisarmLeaseConfigInTx flips the store's lease.auto config to off. Pair with
// ClearArmedLeasesInTx per table inside the same transaction so turning
// stamping off and removing the existing reclaim exposure land together; run
// ClearArmedLeasesInTx again in follow-up transactions until it clears zero
// rows — a claim transaction that read lease.auto before the flip committed
// can stamp a lease that the first sweep's snapshot never saw (disjoint rows,
// so row_lock forces no conflict), and the bounded re-sweep is what closes
// that window.
func DisarmLeaseConfigInTx(ctx context.Context, tx DBTX) error {
	if err := SetConfigInTx(ctx, tx, LeaseAutoConfigKey, "off"); err != nil {
		return fmt.Errorf("set %s=off: %w", LeaseAutoConfigKey, err)
	}
	return nil
}

// ClearArmedLeasesInTx NULLs the lease columns on every in_progress leased
// row in the given table without releasing anything: status/assignee are
// untouched and the fence does not move — disarming is lease bookkeeping,
// not an ownership transition. It cannot distinguish explicitly requested
// leases from auto-stamped ones; every armed lease in the table is cleared.
// row_lock is rewritten so a racing heartbeat/reclaim conflicts rather than
// cell-merging.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func ClearArmedLeasesInTx(ctx context.Context, tx DBTX, issueTable string) (int64, error) {
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET lease_expires_at = NULL, heartbeat_at = NULL, row_lock = ?, updated_at = ?
		WHERE status = 'in_progress' AND lease_expires_at IS NOT NULL
	`, issueTable), freshRowLock(), time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("disarm leases in %s: %w", issueTable, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("disarm rows affected: %w", err)
	}
	return n, nil
}

// ReclaimExpiredLeasesInTx reverts in_progress issues whose lease has gone stale
// back to ready: status → open, assignee cleared, started_at cleared, and a
// fresh row_lock so the reclaim conflicts with a racing heartbeat/close on the
// same row (see freshRowLock). An issue is stale when its lease_expires_at is
// non-null and strictly before cutoff. Callers pass cutoff = now - graceWindow
// (the supervisor uses graceWindow = 2×TTL) so only leases that expired a safe
// margin ago — i.e. workers that are almost certainly dead — are reclaimed.
//
// Reclaim is tier-complete: it sweeps both the permanent issues table and the
// wisps table (which holds durable no_history work), recording recovery
// events in the tier's own event table. Only rows carrying a lease are ever
// touched — with requested-lease semantics (lease.auto off), unleased claims
// are invisible to the reaper regardless of tier. Each result reports the
// tier and the row's post-bump claim_fence so the previous holder is fenced
// out and callers can project recovery correctly. The caller owns Dolt
// versioning.
func ReclaimExpiredLeasesInTx(ctx context.Context, tx DBTX, cutoff time.Time, actor string) ([]types.ReclaimedLease, error) {
	var reclaimed []types.ReclaimedLease
	for _, tier := range []struct{ issueTable, eventTable string }{
		{"issues", "events"},
		{"wisps", "wisp_events"},
	} {
		got, err := reclaimExpiredInTable(ctx, tx, tier.issueTable, tier.eventTable, cutoff, actor)
		if err != nil {
			return nil, err
		}
		reclaimed = append(reclaimed, got...)
	}
	return reclaimed, nil
}

//nolint:gosec // G201: table names are the hardcoded tier constants above
func reclaimExpiredInTable(ctx context.Context, tx DBTX, issueTable, eventTable string, cutoff time.Time, actor string) ([]types.ReclaimedLease, error) {
	// Snapshot the stale set first so we can report exactly which issues we
	// reverted and record per-issue recovery events. The UPDATE below repeats
	// the predicate, so an issue that a concurrent heartbeat rescued between the
	// SELECT and the UPDATE is simply skipped (0 rows) — it never appears as
	// reclaimed.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, COALESCE(assignee, '') FROM %s
		WHERE status = 'in_progress'
		  AND lease_expires_at IS NOT NULL
		  AND lease_expires_at < ?
	`, issueTable), cutoff)
	if err != nil {
		return nil, fmt.Errorf("scan for stale leases in %s: %w", issueTable, err)
	}
	var stale []types.ReclaimedLease
	for rows.Next() {
		var r types.ReclaimedLease
		if err := rows.Scan(&r.ID, &r.PreviousOwner); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stale lease row: %w", err)
		}
		r.Tier = issueTable
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
	for i := range stale {
		r := &stale[i]
		// Re-check the predicate inside the UPDATE so a heartbeat that landed
		// after the snapshot (pushing lease_expires_at back into the future, or
		// the row already closed) cannot be clobbered. row_lock makes the racing
		// writer conflict; this WHERE makes a winning racer's rescue stick.
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET status = 'open', assignee = NULL, holder_token = '', started_at = NULL,
			    lease_expires_at = NULL, heartbeat_at = NULL,
			    claim_fence = claim_fence + 1,
			    updated_at = ?, row_lock = ?
			WHERE id = ? AND status = 'in_progress'
			  AND lease_expires_at IS NOT NULL AND lease_expires_at < ?
		`, issueTable), time.Now().UTC(), freshRowLock(), r.ID, cutoff)
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
		// Report the post-bump fence so the caller holds the value that fences
		// out the previous holder.
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT claim_fence FROM %s WHERE id = ?`, issueTable), r.ID).Scan(&r.Fence); err != nil {
			return nil, fmt.Errorf("read post-reclaim fence for %s: %w", r.ID, err)
		}
		if err := RecordFullEventInTable(ctx, tx, eventTable, r.ID, types.EventLeaseReclaimed, actor,
			r.PreviousOwner, ""); err != nil {
			return nil, fmt.Errorf("record reclaim event for %s: %w", r.ID, err)
		}
		reclaimed = append(reclaimed, *r)
	}
	return reclaimed, nil
}
