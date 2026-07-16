//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// ClaimIssue atomically claims an issue using compare-and-swap semantics.
// Delegates SQL work to issueops; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) ClaimIssue(ctx context.Context, id string, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := issueops.ClaimIssueInTx(ctx, tx, id, actor)
		return err
	})
}

// ClaimReadyIssue atomically claims the first ready issue matching filter.
func (s *EmbeddedDoltStore) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	var claimed *types.Issue
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		claimed, err = issueops.ClaimReadyIssueInTx(ctx, tx, filter, actor)
		return err
	})
	return claimed, err
}

// UnclaimIssue atomically unclaims an issue by clearing the assignee
// and resetting status to "open". Records an "unclaimed" event.
// Delegates SQL work to issueops; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) UnclaimIssue(ctx context.Context, id string, actor string, force bool) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.UnclaimIssueInTx(ctx, tx, id, actor, force)
	})
}

// UpdateIssue updates fields on an issue.
// Delegates SQL work to issueops; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Validate metadata against schema before routing.
	if rawMeta, ok := updates["metadata"]; ok {
		metadataStr, err := storage.NormalizeMetadataValue(rawMeta)
		if err != nil {
			return fmt.Errorf("invalid metadata: %w", err)
		}
		if err := issueops.ValidateMetadataIfConfigured(json.RawMessage(metadataStr)); err != nil {
			return err
		}
	}

	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, actor)
		return err
	})
}

// HeartbeatIssue refreshes the lease on an issue actor holds in_progress.
// Delegates SQL work to issueops; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) HeartbeatIssue(ctx context.Context, id, actor string) error {
	// Tier-complete: issueops routes wisp-table rows (durable no_history work
	// included) to their own table; unleased rows get ErrUnleased there.
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.HeartbeatIssueInTx(ctx, tx, id, actor)
	})
}

// ReclaimExpiredLeases reverts in_progress issues whose lease expired more than
// olderThan ago back to ready, recovering work stranded by dead workers.
func (s *EmbeddedDoltStore) ReclaimExpiredLeases(ctx context.Context, olderThan time.Duration, actor string) ([]types.ReclaimedLease, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	var reclaimed []types.ReclaimedLease
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		reclaimed, err = issueops.ReclaimExpiredLeasesInTx(ctx, tx, cutoff, actor)
		return err
	})
	return reclaimed, err
}

// DisarmAutoLeases sets lease.auto=off and NULLs armed leases on existing
// in_progress rows in both tiers, without releasing them. A bounded
// follow-up sweep catches claims whose transactions read lease.auto before
// the flip committed (see DoltStore.DisarmAutoLeases).
func (s *EmbeddedDoltStore) DisarmAutoLeases(ctx context.Context) (int64, error) {
	sweep := func(flip bool) (int64, error) {
		var swept int64
		err := s.withConn(ctx, true, func(tx *sql.Tx) error {
			swept = 0
			if flip {
				if err := issueops.DisarmLeaseConfigInTx(ctx, tx); err != nil {
					return err
				}
			}
			for _, table := range []string{"issues", "wisps"} {
				n, err := issueops.ClearArmedLeasesInTx(ctx, tx, table)
				if err != nil {
					return err
				}
				swept += n
			}
			return nil
		})
		return swept, err
	}
	total, err := sweep(true)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		n, err := sweep(false)
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			break
		}
	}
	return total, nil
}

// RenewLeases renews the given (id, fence) leases in one transaction.
func (s *EmbeddedDoltStore) RenewLeases(ctx context.Context, refs []storage.LeaseRef, ttl time.Duration) ([]storage.LeaseRenewalResult, error) {
	var out []storage.LeaseRenewalResult
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		out, err = issueops.RenewLeasesInTx(ctx, tx, refs, ttl)
		return err
	})
	return out, err
}

// CountActiveClaimsByOwner counts in_progress claims held by owner across both
// tiers.
func (s *EmbeddedDoltStore) CountActiveClaimsByOwner(ctx context.Context, owner string) (int, error) {
	var n int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountActiveClaimsByOwnerInTx(ctx, tx, owner)
		return err
	})
	return n, err
}

// ReopenIssue reopens a closed issue, setting status to open and clearing
// closed_at and defer_until. If reason is non-empty, it is recorded as a comment.
// Wraps UpdateIssue; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
	updates := map[string]interface{}{
		"status":      string(types.StatusOpen),
		"defer_until": nil,
	}
	if err := s.UpdateIssue(ctx, id, updates, actor); err != nil {
		return err
	}
	if reason != "" {
		if err := s.AddComment(ctx, id, actor, reason); err != nil {
			return fmt.Errorf("reopen comment: %w", err)
		}
	}
	return nil
}

// UpdateIssueType changes the issue_type field of an issue.
// Wraps UpdateIssue; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error {
	return s.UpdateIssue(ctx, id, map[string]interface{}{"issue_type": issueType}, actor)
}

// CloseIssue closes an issue with a reason.
// Delegates SQL work to issueops; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueInTx(ctx, tx, id, reason, actor, session)
		return err
	})
}

// IsBlocked checks if an issue is blocked by active dependencies.
func (s *EmbeddedDoltStore) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	var blocked bool
	var blockers []string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		blocked, blockers, err = issueops.IsBlockedInTx(ctx, tx, issueID)
		return err
	})
	return blocked, blockers, err
}

// GetNewlyUnblockedByClose finds issues that become unblocked when closedIssueID is closed.
func (s *EmbeddedDoltStore) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetNewlyUnblockedByCloseInTx(ctx, tx, closedIssueID)
		return err
	})
	return result, err
}
