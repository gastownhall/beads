package issueops

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
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
//
// The exemptions are load-bearing in the OTHER direction too (analysis
// absorbed from gastownhall/beads#4682, Julian Knutsen): row_lock doubles as
// the RowVersion optimistic-concurrency token (types.Issue.RowVersion), so a
// path that reminted it WITHOUT changing the content a CAS holder cares about
// would spuriously fail honest ExpectedVersion checks. The is_blocked
// recompute is the canonical case: it is a denormalized aux-marker refresh
// that already goes out of its way to preserve updated_at
// (blocked_state.go), and bumping the token there would clobber a concurrent
// whole-row CAS over a cell derived from OTHER rows. (The PR's second
// exemption of this kind, the lease heartbeat, no longer arises here — since
// bd-lrgn1 heartbeats never touch the issues row at all.) So the invariant
// cuts both ways: status/ownership writes MUST remint, aux-marker writes MUST
// NOT. The remint direction is enforced at build time by
// TestAllIssueRowWritesStampRowLock (row_lock_guard_test.go), whose exemption
// markers are the machine-readable form of this list — widen the stamping
// policy there first, not by ad-hoc stamps.
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

// nodeIDContextKey overrides config.NodeID() for a single call. Used by tests
// that have to be two replicas at once (one process, one database, two
// granting nodes); unset in normal use, where every call resolves the real
// machine identity.
type nodeIDContextKey struct{}

// WithNodeID returns a context whose lease writes and reclaim guard treat node
// as this replica's identity instead of config.NodeID().
func WithNodeID(ctx context.Context, node string) context.Context {
	return context.WithValue(ctx, nodeIDContextKey{}, node)
}

// NodeID resolves the granting-replica identity for the current lease
// operation: the context override (WithNodeID) if present, else the
// deployment's node identity (config.NodeID()). "" means the provenance of a
// lease granted here is unknown — see ReclaimExpiredLeasesInTx for how the
// guard degrades.
func NodeID(ctx context.Context) string {
	if node, ok := ctx.Value(nodeIDContextKey{}).(string); ok {
		return node
	}
	return config.NodeID()
}

// UpsertLeaseInTx grants or re-grants the lease on an issue to holder: a
// future expiry, a now heartbeat. The lease row lives in the ephemeral leases
// table (dolt_ignored on the Dolt backend, bd-lrgn1), NOT on the issues row,
// so granting or renewing it mints no Dolt commit and no history. Leases are
// deliberately node-local: they are only enforceable on the replica that
// granted them; cross-machine claim VISIBILITY rides status/assignee on the
// issues row, which still commits.
//
// granted_node records WHICH replica that is (config.NodeID(), wy-jpd3.7).
// A re-grant through this node makes this node the granting replica, so the
// value is rewritten unconditionally alongside holder/granted_at.
//
// INVARIANT: a leases row exists if and only if its issue is a live claim
// (in_progress with the row's holder as assignee) on this node. Every path
// that ends or transfers a claim — close, unclaim, reclaim, delete, a generic
// update that changes status/assignee, an import that accepts a newer
// non-claimed snapshot — must delete the lease row (DeleteLeaseInTx). Wisps
// are never leased (see testHeartbeatWisp) and never get a row here.
func UpsertLeaseInTx(ctx context.Context, tx DBTX, id, holder string, now time.Time, ttl time.Duration) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at, granted_node)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			holder = VALUES(holder),
			granted_at = VALUES(granted_at),
			lease_expires_at = VALUES(lease_expires_at),
			heartbeat_at = VALUES(heartbeat_at),
			granted_node = VALUES(granted_node)
	`, id, holder, now, now.Add(ttl), now, NodeID(ctx))
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
// A restored lease keeps the granting replica the SNAPSHOT names
// (issue.LeaseGrantedNode), never this node — this is the one path that can
// materialize a foreign lease row locally, and mislabelling it as local is
// exactly the cross-replica reclaim hazard (wy-jpd3.7). A snapshot written by
// a pre-wy-jpd3.7 binary carries no node, which restores as "" (unknown) and
// therefore behaves exactly as it did before this change.
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
				INSERT INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at, granted_node)
				VALUES (?, ?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE
					holder = IF(leases.lease_expires_at >= ?, leases.holder, VALUES(holder)),
					granted_at = IF(leases.lease_expires_at >= ?, leases.granted_at, VALUES(granted_at)),
					heartbeat_at = IF(leases.lease_expires_at >= ?, leases.heartbeat_at, VALUES(heartbeat_at)),
					granted_node = IF(leases.lease_expires_at >= ?, leases.granted_node, VALUES(granted_node)),
					lease_expires_at = IF(leases.lease_expires_at >= ?, leases.lease_expires_at, VALUES(lease_expires_at))
			`, issue.ID, assignee, grantedAt, *issue.LeaseExpiresAt, heartbeatAt, issue.LeaseGrantedNode,
				now, now, now, now, now)
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
	// granted_node is backfilled, never overwritten: a lease row whose
	// provenance is unknown (granted before ignored migration 0016, or before
	// this replica was named) is being kept alive through THIS node's store,
	// which is the node that can enforce it. A row that already names a
	// replica keeps it — a heartbeat proves the holder is alive, not that the
	// lease moved. NodeID is "" unless an operator configured it, so on an
	// unnamed deployment this backfill writes '' over '' and changes nothing:
	// the fail-open default cannot be silently converted to fail-closed.
	result, err := tx.ExecContext(ctx, `
		UPDATE leases SET lease_expires_at = ?, heartbeat_at = ?,
			granted_node = IF(COALESCE(granted_node, '') = '', ?, granted_node)
		WHERE issue_id = ? AND holder = ?
	`, now.Add(leaseTTL(ctx)), now, NodeID(ctx), id, actor)
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
	// DELIBERATELY NOT JOURNALED — do not add an emit here. A heartbeat is a
	// high-frequency lease keepalive: it writes only the clone-local `leases`
	// table (lease-liveness state), never a durable bead field. Journaling it
	// would put a full-snapshot write plus the shared seq-counter serialization
	// on the hottest write in the fleet, and a replay consumer gains nothing —
	// lease state lives on the working-set plane and expires on its own. Lease
	// RECLAIM is the opposite case and does journal: it clears assignee and
	// reverts status, which is durable bead state. The decision is pinned by
	// journalExemptMutations in journal_completeness_test.go.
	return nil
}

// warnReplica writes a replica-guard audit line to STDERR (never stdout —
// bd reclaim --json owns stdout and a stray line there breaks every machine
// consumer), suppressed only by quiet mode.
func warnReplica(format string, args ...any) {
	if debug.IsQuiet() {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

// reportForeignSkips names the stale leases the replica guard declined.
// Best-effort and never fatal: this is an audit courtesy on the reclaim path,
// not a correctness step, so a failed query is swallowed rather than allowed
// to abort a reclaim that is otherwise fine.
func reportForeignSkips(ctx context.Context, tx DBTX, cutoff time.Time, filter types.ReclaimFilter, localNode string) {
	scopeSQL, scopeArgs := sqlbuild.ReclaimScopeSQL(filter, sqlbuild.IssuesFilterTables, "i")
	args := append([]any{cutoff, localNode}, scopeArgs...)
	rows, err := tx.QueryContext(ctx, `
		SELECT l.issue_id, COALESCE(l.granted_node, ''), COALESCE(i.assignee, '') FROM leases l
		JOIN issues i ON i.id = l.issue_id
		WHERE i.status = 'in_progress'
		  AND l.lease_expires_at < ?
		  AND COALESCE(l.granted_node, '') NOT IN ('', ?)
	`+scopeSQL, args...)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, grantedNode, holder string
		if err := rows.Scan(&id, &grantedNode, &holder); err != nil {
			return
		}
		warnReplica("reclaim: skipping %s (held by %s) — lease granted by replica %q, not this node (%q). "+
			"Reap it on that replica, or pass --any-replica if that replica is gone.\n",
			id, holder, grantedNode, localNode)
	}
}

// reclaimReplicaSQL returns the granting-replica predicate for the reclaim
// snapshot and the per-row DELETE, plus its args. Both sites must apply the
// SAME predicate: the DELETE re-checks by id, and skipping the guard there
// would let a foreign lease slip through on the re-check path.
//
// Empty when the guard is disarmed — either explicitly (filter.AnyReplica) or
// because this deployment cannot name itself (localNode == ""), where every
// comparison would be against "" and the guard would degenerate to "reclaim
// only unknown-provenance leases", stranding this node's own work.
func reclaimReplicaSQL(filter types.ReclaimFilter, localNode string) (string, []any) {
	if filter.AnyReplica || localNode == "" {
		return "", nil
	}
	// granted_node '' is unknown provenance and stays eligible (fail-open);
	// only a lease that positively names a different replica is protected.
	//
	// granted_node is deliberately UNQUALIFIED: the snapshot splices this into
	// a `leases l JOIN issues i` (where it resolves to l, since issues has no
	// such column) but the DELETE site splices it into a bare
	// `DELETE FROM leases`, which has no alias to qualify with. Adding a
	// leases-only column to `issues` would make the snapshot ambiguous — give
	// this an alias parameter then, and pass "" from the DELETE.
	return "\n\t\t  AND (COALESCE(granted_node, '') = '' OR granted_node = ?)", []any{localNode}
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
// REPLICA GUARD (wy-jpd3.7). "Through this node" is now enforced, not merely
// implied by the transport. The one path that can materialize a foreign lease
// row locally is RestoreLeaseOnImportInTx (lease fields round-trip the JSONL
// interchange), and a lease imported from another replica carries a liveness
// view that is stale by up to one sync interval — reverting on it can rob a
// worker that is very much alive over there. So a lease whose granted_node
// POSITIVELY names another replica is skipped, and only an explicit
// filter.AnyReplica (bd reclaim --any-replica) reverts it.
//
// The guard is OPT-IN and deliberately fail-open. An empty granted_node (a lease
// row predating ignored migration 0016, or the default state of any
// deployment that has not set config.NodeID()) is treated as local, so an
// upgrade can never strand a stale lease the reaper could previously recover,
// and a single-store deployment — including many hosts sharing one dolt
// sql-server, where there is no sync interval to defend against — keeps
// exactly its old behavior. See config.NodeID for why there is no hostname
// fallback. The invariant the guard cannot enforce, and that every federated
// deployment still owes:
//
//	grace window > sync interval  (and TTL > sync interval)
//
// A TTL or grace shorter than the cadence at which replicas exchange state is
// meaningless across the bridge — the remote view is stale by a full interval
// by construction, so a reaper on the far side decides on liveness data older
// than the lease it is judging. bd reclaim's default grace is 2× the lease
// TTL; raise the TTL (or the grace) above the sync interval, not the other way
// round.
//
// Reclaim only ever touches the permanent issues table: wisps are ephemeral and
// are never leased work. Returns the issues it reverted (id + the owner it took
// the lease from) so the caller can log/emit recovery events. The caller owns
// Dolt versioning.
//
// filter narrows which stale leases are eligible (see types.ReclaimFilter); the
// zero filter keeps the historical global behavior. Scoping is applied to the
// snapshot SELECT only — the per-row DELETE/UPDATE re-checks staleness by id,
// and the ids it re-checks all came from the already-scoped snapshot, so a
// label removed mid-reclaim cannot smuggle an out-of-scope issue into the
// revert.
func ReclaimExpiredLeasesInTx(ctx context.Context, tx DBTX, cutoff time.Time, filter types.ReclaimFilter, actor string) ([]types.ReclaimedLease, error) {
	// Snapshot the stale set first so we can report exactly which issues we
	// reverted and record per-issue recovery events. The DELETE below repeats
	// the expiry predicate, so an issue that a concurrent heartbeat rescued
	// between the SELECT and the DELETE is simply skipped (0 rows) — it never
	// appears as reclaimed.
	scopeSQL, scopeArgs := sqlbuild.ReclaimScopeSQL(filter, sqlbuild.IssuesFilterTables, "i")
	localNode := NodeID(ctx)
	replicaSQL, replicaArgs := reclaimReplicaSQL(filter, localNode)
	args := append([]any{cutoff}, replicaArgs...)
	args = append(args, scopeArgs...)
	rows, err := tx.QueryContext(ctx, `
		SELECT l.issue_id, COALESCE(i.assignee, ''), COALESCE(l.granted_node, '') FROM leases l
		JOIN issues i ON i.id = l.issue_id
		WHERE i.status = 'in_progress'
		  AND l.lease_expires_at < ?
	`+replicaSQL+scopeSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("scan for stale leases: %w", err)
	}
	var stale []types.ReclaimedLease
	// Provenance of each snapshot row, parallel to stale, for the
	// --any-replica audit line below. Kept beside the slice rather than on
	// types.ReclaimedLease: it is a reclaim-time diagnostic, not part of the
	// recovery record every backend returns to callers.
	staleNodes := map[string]string{}
	for rows.Next() {
		var r types.ReclaimedLease
		var grantedNode string
		if err := rows.Scan(&r.ID, &r.PreviousOwner, &grantedNode); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stale lease row: %w", err)
		}
		staleNodes[r.ID] = grantedNode
		stale = append(stale, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate stale leases: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close stale lease rows: %w", err)
	}
	// Audit the other side of the guard: a stale lease this reaper declined
	// because another replica granted it. Silence here would look identical to
	// "nothing to recover" while a genuinely dead remote worker's unit sits
	// in_progress forever, so name it (the operator's remedy is to reap on the
	// granting replica, or --any-replica once that replica is gone for good).
	if replicaSQL != "" {
		reportForeignSkips(ctx, tx, cutoff, filter, localNode)
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
		delArgs := append([]any{r.ID, cutoff}, replicaArgs...)
		res, err := tx.ExecContext(ctx, `
			DELETE FROM leases WHERE issue_id = ? AND lease_expires_at < ?
		`+replicaSQL, delArgs...)
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
		// Journal the lease reclaim as an update (assignee cleared, status
		// reverted to open) so a replayer sees the claim released. Emitted past
		// both re-checks, so only reverts that actually happened are recorded.
		if err := RecordEventInTx(ctx, tx, EventUpdate, r.ID); err != nil {
			return nil, err
		}
		// Logged HERE, past both re-checks, so the audit trail records reverts
		// that actually happened: a heartbeat landing after the snapshot makes
		// the DELETE match nothing, and a "reverting X" line printed up there
		// would be a lie the operator has no way to catch.
		// localNode "" is skipped, not printed as `not this node ("")`: on an
		// unnamed deployment the guard was never armed, so there is no
		// override to audit — every lease was already eligible.
		if node := staleNodes[r.ID]; filter.AnyReplica && localNode != "" && node != "" && node != localNode {
			warnReplica("reclaim: --any-replica reverted %s (held by %s) — lease was granted by replica %q, not this node (%q)\n",
				r.ID, r.PreviousOwner, node, localNode)
		}
		reclaimed = append(reclaimed, r)
	}
	return reclaimed, nil
}
