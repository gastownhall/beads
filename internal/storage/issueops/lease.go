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
// that touch DIFFERENT cells of the same issue row (a reclaim writing status,
// a close writing closed_at) merge silently instead of conflicting — which
// would let a reclaim quietly revert an issue the owner just closed. By having
// every status/ownership-mutating path rewrite this one shared cell to a fresh
// random value, those writers always collide on row_lock, surfacing the
// 1213/1205 serialization conflict that withRetryTx replays. The value's only
// job is to differ from whatever a concurrent writer wrote, so any source of
// entropy works; we use crypto/rand to avoid seeding concerns. Never 0 (the
// column default) so a freshly-claimed row is always distinguishable from a
// never-touched one.
//
// INVARIANT: any path that mutates status, assignee, or started_at on an
// in_progress issue MUST rewrite row_lock — that is the set the reclaim/close
// races care about (claim, close, updateIssueInTx, reclaim, unclaim all do).
// Paths that touch only orthogonal cells (is_blocked, compaction_level,
// dependency metadata, rename, or reopen — which acts on closed rows) are safe
// to merge with a reclaim and intentionally do NOT rewrite it. Heartbeats no
// longer touch the issues row at all (bd-lrgn1): the lease lives in the
// ephemeral leases table, where a racing heartbeat and reclaim contend on the
// SAME lease row and conflict without any help. Adding a new path that sets
// status/assignee outside updateIssueInTx without rewriting row_lock would
// silently reintroduce the zombie-merge bug.
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

// RowLockClause returns the SET-clause fragment and arg that rewrite row_lock
// to a fresh value. Append to any UPDATE that mutates status/assignee/
// started_at on an issues row (see the freshRowLock invariant). Exported for
// the proxied-server (uow) claim path in internal/storage/domain/db, which
// builds its own claim UPDATE rather than calling ClaimIssueInTx.
func RowLockClause() (string, []interface{}) {
	return "row_lock = ?", []interface{}{freshRowLock()}
}

// FreshRowLock returns a fresh non-zero row_lock token for an INSERT column
// list. Exported for the proxied-server (uow) create path in
// internal/storage/domain/db, which builds its own INSERT rather than calling
// InsertIssueIntoTable. Every create must stamp a non-zero row_lock so the
// RowVersion optimistic-concurrency token is live from the first write, exactly
// like the classic insert (see the freshRowLock invariant and types.Issue.RowVersion).
func FreshRowLock() int64 {
	return freshRowLock()
}

// LeaseTTL is the exported form of leaseTTL: it resolves the lease TTL for the
// current claim from the context (WithLeaseTTL) or falls back to
// DefaultLeaseTTL.
func LeaseTTL(ctx context.Context) time.Duration {
	return leaseTTL(ctx)
}

// UpsertLeaseInTx grants or re-grants the lease on an issue to holder: a
// future expiry, a now heartbeat. The lease row lives in the ephemeral leases
// table (dolt_ignored on the Dolt backend, bd-lrgn1), NOT on the issues row,
// so granting or renewing it mints no Dolt commit and no history. Leases are
// deliberately node-local: they are only enforceable on the replica that
// granted them; cross-machine claim VISIBILITY rides status/assignee on the
// issues row, which still commits.
//
// INVARIANT: a leases row exists if and only if its issue is a live claim
// (in_progress with the row's holder as assignee) on this node. Every path
// that ends or transfers a claim — close, unclaim, reclaim, delete, a generic
// update that changes status/assignee, an import that accepts a newer
// non-claimed snapshot — must delete the lease row (DeleteLeaseInTx). Wisps
// are never leased (see testHeartbeatWisp) and never get a row here.
func UpsertLeaseInTx(ctx context.Context, tx DBTX, id, holder string, now time.Time, ttl time.Duration) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			holder = VALUES(holder),
			granted_at = VALUES(granted_at),
			lease_expires_at = VALUES(lease_expires_at),
			heartbeat_at = VALUES(heartbeat_at)
	`, id, holder, now, now.Add(ttl), now)
	if err != nil {
		return fmt.Errorf("upsert lease for %s: %w", id, err)
	}
	return nil
}

// DeleteLeaseInTx removes the lease row for an issue, if any. Call from every
// path that ends or transfers a claim (see the UpsertLeaseInTx invariant).
// Deleting a lease that does not exist is a no-op, so callers may invoke it
// unconditionally — including for wisp IDs, which never have lease rows.
func DeleteLeaseInTx(ctx context.Context, tx DBTX, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM leases WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete lease for %s: %w", id, err)
	}
	return nil
}

// RestoreLeaseOnImportInTx reconciles an issue's lease row after an
// import/upsert wrote the issue row (protocol L1.2: lease fields round-trip
// the JSONL interchange, wy-urlct — bd-lrgn1 moved them from issues columns
// to the ephemeral leases table).
//
// Two duties, both keyed off the STORED row (the winner after any stale-guard
// merge), never the snapshot alone:
//
//   - restore: when the snapshot carried a lease and the stored row is a live
//     claim, upsert the lease row — but NEVER clobber a live (unexpired) local
//     lease with snapshot data. Leases are node-local; the local grant is
//     always more authoritative than a replicated timestamp.
//   - reconcile: when the accepted state ended or transferred the claim, drop
//     any now-orphaned local lease row so the UpsertLeaseInTx invariant
//     (lease row ⇔ live claim) holds.
//
// Wisps are never leased; callers route them away before calling this.
func RestoreLeaseOnImportInTx(ctx context.Context, tx DBTX, issue *types.Issue, isNew bool) error {
	now := time.Now().UTC()

	if issue.LeaseExpiresAt != nil {
		var status, assignee string
		err := tx.QueryRowContext(ctx,
			"SELECT status, COALESCE(assignee, '') FROM issues WHERE id = ?", issue.ID,
		).Scan(&status, &assignee)
		if err != nil {
			return fmt.Errorf("read stored row for lease restore of %s: %w", issue.ID, err)
		}
		if status == string(types.StatusInProgress) && assignee != "" {
			grantedAt := now
			heartbeatAt := now
			if issue.HeartbeatAt != nil {
				grantedAt = *issue.HeartbeatAt
				heartbeatAt = *issue.HeartbeatAt
			}
			// Assignment order matters: lease_expires_at is the liveness
			// comparison column and ON DUPLICATE KEY UPDATE assignments are
			// evaluated in order, so it must be reassigned LAST.
			_, err := tx.ExecContext(ctx, `
				INSERT INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at)
				VALUES (?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE
					holder = IF(leases.lease_expires_at >= ?, leases.holder, VALUES(holder)),
					granted_at = IF(leases.lease_expires_at >= ?, leases.granted_at, VALUES(granted_at)),
					heartbeat_at = IF(leases.lease_expires_at >= ?, leases.heartbeat_at, VALUES(heartbeat_at)),
					lease_expires_at = IF(leases.lease_expires_at >= ?, leases.lease_expires_at, VALUES(lease_expires_at))
			`, issue.ID, assignee, grantedAt, *issue.LeaseExpiresAt, heartbeatAt,
				now, now, now, now)
			if err != nil {
				return fmt.Errorf("restore lease for %s: %w", issue.ID, err)
			}
		}
	}

	// An upsert over an existing row may have ended or transferred the claim
	// (e.g. a newer snapshot closed the issue): drop a lease row that no
	// longer matches a live claim by its holder.
	if !isNew {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM leases WHERE issue_id = ?
			  AND NOT EXISTS (
				SELECT 1 FROM issues i
				WHERE i.id = ? AND i.status = 'in_progress' AND i.assignee = leases.holder
			  )
		`, issue.ID, issue.ID)
		if err != nil {
			return fmt.Errorf("reconcile lease for %s: %w", issue.ID, err)
		}
	}
	return nil
}

// HeartbeatIssueInTx proves the lease owner is still alive: it pushes
// lease_expires_at forward by the TTL and stamps heartbeat_at = now on the
// issue's lease row. Only the current holder may heartbeat — a heartbeat from
// anyone else, or on an issue whose lease is gone (closed, unclaimed,
// reclaimed, or never leased — wisps), affects no rows and returns
// storage.ErrNotClaimable / ErrAlreadyClaimed so the caller learns its lease
// is gone.
//
// The write touches ONLY the leases table (ephemeral, dolt_ignored): a
// heartbeat mints no Dolt commit and no history, and deliberately does NOT
// stamp issues.updated_at — updated_at keeps its merge/LWW meaning and bd
// stale consults leases.heartbeat_at for in_progress rows instead (bd-lrgn1).
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func HeartbeatIssueInTx(ctx context.Context, tx DBTX, id, actor string) error {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE leases SET lease_expires_at = ?, heartbeat_at = ?
		WHERE issue_id = ? AND holder = ?
	`, now.Add(leaseTTL(ctx)), now, id, actor)
	if err != nil {
		return fmt.Errorf("failed to heartbeat issue: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		// No lease row. Disambiguate from the issue row: gone
		// (closed/reopened/reclaimed), not-found, owned by someone else, or a
		// wisp (never leased).
		isWisp := IsActiveWispInTx(ctx, tx, id)
		issueTable, _, _, _ := WispTableRouting(isWisp)
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
		if !isWisp && assignee == actor && status == string(types.StatusInProgress) {
			// The caller genuinely holds the claim but has no lease row — e.g.
			// the claim was hand-doled through a generic update (which never
			// arms a lease, bd-9hpgf) and the worker is now opting into lease
			// semantics. A real worker's heartbeat re-arms recovery.
			return UpsertLeaseInTx(ctx, tx, id, actor, now, leaseTTL(ctx))
		}
		return fmt.Errorf("%w: %s status %s", storage.ErrNotClaimable, id, status)
	}
	return nil
}

// ReclaimExpiredLeasesInTx reverts in_progress issues whose lease has gone stale
// back to ready: the lease row is deleted, then status → open, assignee cleared,
// started_at cleared, and a fresh row_lock so the reclaim conflicts with a
// racing close/update on the same issues row (see freshRowLock). An issue is
// stale when its lease row's lease_expires_at is strictly before cutoff.
// Callers pass cutoff = now - graceWindow (the supervisor uses graceWindow =
// 2×TTL) so only leases that expired a safe margin ago — i.e. workers that are
// almost certainly dead — are reclaimed.
//
// Leases are node-local (the leases table is dolt_ignored and does not
// replicate), so a reclaim can only recover claims granted through this node —
// which is the only place the lease was ever enforceable anyway.
//
// Reclaim only ever touches the permanent issues table: wisps are ephemeral and
// are never leased work. Returns the issues it reverted (id + the owner it took
// the lease from) so the caller can log/emit recovery events. The caller owns
// Dolt versioning.
func ReclaimExpiredLeasesInTx(ctx context.Context, tx DBTX, cutoff time.Time, actor string) ([]types.ReclaimedLease, error) {
	// Snapshot the stale set first so we can report exactly which issues we
	// reverted and record per-issue recovery events. The DELETE below repeats
	// the expiry predicate, so an issue that a concurrent heartbeat rescued
	// between the SELECT and the DELETE is simply skipped (0 rows) — it never
	// appears as reclaimed.
	rows, err := tx.QueryContext(ctx, `
		SELECT l.issue_id, COALESCE(i.assignee, '') FROM leases l
		JOIN issues i ON i.id = l.issue_id
		WHERE i.status = 'in_progress'
		  AND l.lease_expires_at < ?
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
		// Re-check the expiry inside the DELETE so a heartbeat that landed
		// after the snapshot (pushing lease_expires_at back into the future)
		// cannot be clobbered: heartbeat and reclaim contend on this same lease
		// row, so one of a racing pair is forced to retry, and a winning
		// rescuer's pushed-out expiry makes this DELETE match nothing.
		res, err := tx.ExecContext(ctx, `
			DELETE FROM leases WHERE issue_id = ? AND lease_expires_at < ?
		`, r.ID, cutoff)
		if err != nil {
			return nil, fmt.Errorf("reclaim %s: %w", r.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("reclaim %s rows affected: %w", r.ID, err)
		}
		if n == 0 {
			continue // rescued by a concurrent heartbeat — leave it be
		}
		// Revert the issue itself. status is re-checked so a row that stopped
		// being in_progress under us (closed) is left alone; row_lock makes a
		// concurrent close/update conflict at commit time rather than
		// cell-merge with this write.
		res, err = tx.ExecContext(ctx, `
			UPDATE issues
			SET status = 'open', assignee = NULL, started_at = NULL,
			    updated_at = ?, row_lock = ?
			WHERE id = ? AND status = 'in_progress'
		`, time.Now().UTC(), freshRowLock(), r.ID)
		if err != nil {
			return nil, fmt.Errorf("reclaim %s: %w", r.ID, err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("reclaim %s rows affected: %w", r.ID, err)
		}
		if n == 0 {
			continue // no longer in_progress — its lease row was stale anyway
		}
		if err := RecordFullEventInTable(ctx, tx, "events", r.ID, types.EventLeaseReclaimed, actor,
			r.PreviousOwner, ""); err != nil {
			return nil, fmt.Errorf("record reclaim event for %s: %w", r.ID, err)
		}
		reclaimed = append(reclaimed, r)
	}
	return reclaimed, nil
}
