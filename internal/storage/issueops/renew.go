package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// RenewLeasesInTx renews the leases named by refs, each keyed on (id, fence),
// in the caller's transaction, returning a typed outcome per ref (in refs
// order). A renewal renews only while the row is in_progress, still leased,
// and its claim_fence still equals the ref's fence — so a claim whose
// ownership moved since the caller's snapshot is reported lost, not silently
// renewed, and an unleased claim is reported unleased rather than armed.
// Tier-aware: each ref routes to issues or wisps.
//
// The caller owns Dolt versioning and chunking (see the store-level
// RenewLeasesChunked, which bounds the row_lock rewrite set per transaction).
func RenewLeasesInTx(ctx context.Context, tx DBTX, refs []storage.LeaseRef, ttl time.Duration) ([]storage.LeaseRenewalResult, error) {
	now := time.Now().UTC()
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	leaseClause, leaseArgs := leaseSetClause(now, ttl)

	out := make([]storage.LeaseRenewalResult, 0, len(refs))
	for _, ref := range refs {
		isWisp := IsActiveWispInTx(ctx, tx, ref.ID)
		issueTable, _, _, _ := WispTableRouting(isWisp)

		args := append([]interface{}{}, leaseArgs...)
		args = append(args, ref.ID, ref.Fence)
		//nolint:gosec // G201: issueTable is a hardcoded routing constant
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s SET %s
			WHERE id = ? AND status = 'in_progress'
			  AND claim_fence = ? AND lease_expires_at IS NOT NULL
		`, issueTable, leaseClause), args...)
		if err != nil {
			return nil, fmt.Errorf("renew %s: %w", ref.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("renew %s rows affected: %w", ref.ID, err)
		}
		if n > 0 {
			out = append(out, storage.LeaseRenewalResult{ID: ref.ID, Outcome: storage.LeaseRenewed})
			continue
		}
		outcome, err := renewalMiss(ctx, tx, issueTable, ref)
		if err != nil {
			return nil, fmt.Errorf("renew %s disambiguation: %w", ref.ID, err)
		}
		out = append(out, storage.LeaseRenewalResult{ID: ref.ID, Outcome: outcome})
	}
	return out, nil
}

// renewalMiss disambiguates a zero-row renewal: not-found, fence superseded
// (lost), or owned-but-unleased. A read error is PROPAGATED, never mapped to
// an outcome: reporting not_found on a transient/schema-skew read failure
// would tell the orchestrator a live claim is gone — inviting it to drop or
// reassign the claim (duplicate execution, the exact failure fencing exists
// to prevent). The outer write path replays transients via withRetryTx.
//
//nolint:gosec // G201: issueTable is a hardcoded routing constant
func renewalMiss(ctx context.Context, tx DBTX, issueTable string, ref storage.LeaseRef) (storage.LeaseRenewalOutcome, error) {
	var status string
	var fence int64
	var leaseExpires sql.NullTime
	err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT status, claim_fence, lease_expires_at FROM %s WHERE id = ?`, issueTable), ref.ID).
		Scan(&status, &fence, &leaseExpires)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.LeaseRenewalNotFound, nil
	}
	if err != nil {
		return "", err
	}
	if fence != ref.Fence || status != "in_progress" {
		return storage.LeaseRenewalLost, nil
	}
	if !leaseExpires.Valid {
		return storage.LeaseRenewalUnleased, nil
	}
	// In progress, fence matches, leased, yet 0 rows — a concurrent writer
	// changed it between the UPDATE and this read; report lost (the caller
	// re-snapshots next tick).
	return storage.LeaseRenewalLost, nil
}

// CountActiveClaimsByOwnerInTx counts in_progress claims held by owner across
// both tiers.
func CountActiveClaimsByOwnerInTx(ctx context.Context, tx DBTX, owner string) (int, error) {
	total := 0
	for _, table := range []string{"issues", "wisps"} {
		var n int
		//nolint:gosec // G201: table is a hardcoded tier constant
		err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT COUNT(*) FROM %s WHERE status = 'in_progress' AND assignee = ?`, table), owner).Scan(&n)
		if err != nil {
			return 0, fmt.Errorf("count claims in %s: %w", table, err)
		}
		total += n
	}
	return total, nil
}
