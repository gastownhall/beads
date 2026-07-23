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

// UnclaimIssueIfAssignee releases a claim only while the issue is still assigned
// to expectedAssignee (compare-and-swap, the inverse of ClaimIssue). Returns
// storage.ErrAssigneeMismatch, leaving the issue untouched, when the current
// assignee differs. Delegates SQL work to issueops; EmbeddedDolt auto-commits
// the transaction.
func (s *EmbeddedDoltStore) UnclaimIssueIfAssignee(ctx context.Context, id string, actor string, expectedAssignee string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.UnclaimIssueIfAssigneeInTx(ctx, tx, id, actor, expectedAssignee)
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

// UpdateIssueChecked applies the update like UpdateIssue, adding an optional
// optimistic-concurrency precondition: when opts.ExpectedVersion is non-nil the
// update proceeds only if the issue's current RowVersion (row_lock) still equals
// *opts.ExpectedVersion, else it refuses with storage.ErrVersionMismatch. The
// version read and the update share ONE transaction, so a mismatch returns
// before any write and the transaction rolls back with the issue unchanged (a
// true compare-and-swap). nil disables the check, leaving behavior identical to
// UpdateIssue. Delegates SQL work to issueops; EmbeddedDolt auto-commits the
// transaction.
func (s *EmbeddedDoltStore) UpdateIssueChecked(ctx context.Context, id string, updates map[string]interface{}, actor string, opts storage.UpdateIssueOptions) error {
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
		if opts.ExpectedVersion != nil {
			if err := issueops.CheckVersionInTx(ctx, tx, id, *opts.ExpectedVersion); err != nil {
				return err
			}
		}
		_, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, actor)
		return err
	})
}

// HeartbeatIssue refreshes the lease on an issue actor holds in_progress.
// Delegates SQL work to issueops; EmbeddedDolt auto-commits the transaction.
func (s *EmbeddedDoltStore) HeartbeatIssue(ctx context.Context, id, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		if issueops.IsActiveWispInTx(ctx, tx, id) {
			// Wisps are ephemeral and never leased; nothing to heartbeat.
			return fmt.Errorf("%w: %s is ephemeral", storage.ErrNotClaimable, id)
		}
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

// CloseIssueChecked closes an issue but refuses with storage.ErrCloseBlocked
// when it has a live direct blocker unless opts.Force is set, and — when
// opts.ExpectedVersion is non-nil — with storage.ErrVersionMismatch when the
// row's current RowVersion no longer matches (an orthogonal CAS that Force does
// not bypass). Both checks and the close share one transaction, so they are
// atomic (no TOCTOU). Delegates SQL work to issueops; EmbeddedDolt auto-commits
// the transaction.
func (s *EmbeddedDoltStore) CloseIssueChecked(ctx context.Context, id string, actor string, opts storage.CloseIssueOptions) (storage.CloseIssueResult, error) {
	var result storage.CloseIssueResult
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		res, err := issueops.CloseIssueCheckedInTx(ctx, tx, id, opts.Reason, actor, opts.Session, opts.Force, opts.ExpectedVersion)
		if err != nil {
			return err
		}
		result = storage.CloseIssueResult{Unchanged: res.AlreadyClosed}
		return nil
	})
	if err != nil {
		return storage.CloseIssueResult{}, err
	}
	return result, nil
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

// IsBlockedBatch returns the denormalized transitive is_blocked flag for each id
// in one batched read. Delegates to issueops.IsBlockedBatchInTx.
func (s *EmbeddedDoltStore) IsBlockedBatch(ctx context.Context, ids []string) (map[string]bool, error) {
	var result map[string]bool
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.IsBlockedBatchInTx(ctx, tx, ids)
		return err
	})
	return result, err
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
