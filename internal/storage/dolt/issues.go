package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// CreateIssue creates a new issue.
// Delegates SQL work to issueops; handles Dolt versioning for non-ephemeral issues.
func (s *DoltStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("issue must not be nil")
	}

	// Route to wisps table if ephemeral, no-history, or infra type.
	useWispsTable := issue.Ephemeral || issue.NoHistory || s.IsInfraTypeCtx(ctx, issue.IssueType)
	if useWispsTable && !issue.NoHistory {
		issue.Ephemeral = true // infra types get marked ephemeral (legacy behavior)
	}

	var result issueops.CreateIssueResult
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		// SkipPrefixValidation matches legacy behavior: single-issue path does
		// not validate prefixes for explicit IDs.
		bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		})
		if err != nil {
			return err
		}
		result, err = issueops.CreateIssueInTxWithResult(ctx, tx, bc, issue, actor)
		return err
	}); err != nil {
		return err
	}

	// Dolt versioning — wisps and no-history issues skip DOLT_COMMIT.
	if !issue.Ephemeral && !issue.NoHistory {
		if err := s.doltAddAndCommit(ctx, createIssueCommitTables(ctx, issue, result),
			fmt.Sprintf("bd: create %s", issue.ID)); err != nil {
			return err
		}
	}
	return nil
}

func createIssueCommitTables(ctx context.Context, issue *types.Issue, result issueops.CreateIssueResult) []string {
	return sortedDirtyTables(issueops.CreateIssueDirtyTables(ctx, issue, result))
}

func createIssuesCommitTables(ctx context.Context, issues []*types.Issue, result issueops.CreateIssuesResult) []string {
	return sortedDirtyTables(issueops.CreateIssuesDirtyTables(ctx, issues, result))
}

func sortedDirtyTables(dirty map[string]bool) []string {
	if len(dirty) == 0 {
		return nil
	}
	tables := make([]string, 0, len(dirty))
	for table := range dirty {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// CreateIssues creates multiple issues in a single transaction
func (s *DoltStore) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	return s.CreateIssuesWithFullOptions(ctx, issues, actor, storage.BatchCreateOptions{
		OrphanHandling:       storage.OrphanAllow,
		SkipPrefixValidation: false,
	})
}

// CreateIssuesWithFullOptions creates multiple issues with full options control.
// Delegates SQL work to issueops; handles Dolt versioning for non-ephemeral batches.
func (s *DoltStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	if len(issues) == 0 {
		return nil
	}

	// All-wisps fast path: one SQL transaction, no Dolt versioning.
	// Covers both ephemeral issues and no-history issues (both skip DOLT_COMMIT).
	if issueops.AllWisps(issues) {
		for _, issue := range issues {
			if !issue.NoHistory {
				issue.Ephemeral = true
			}
		}
		return s.withRetryTx(ctx, func(tx *sql.Tx) error {
			_, err := issueops.CreateIssuesInTxWithResult(ctx, tx, issues, actor, opts)
			return err
		})
	}

	var result issueops.CreateIssuesResult
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.CreateIssuesInTxWithResult(ctx, tx, issues, actor, opts)
		return err
	}); err != nil {
		return err
	}

	// GH#2455: Stage only the tables we modified, then commit without -A.
	return s.doltAddAndCommit(ctx,
		createIssuesCommitTables(ctx, issues, result),
		fmt.Sprintf("bd: create %d issue(s)", len(issues)))
}

// GetIssue retrieves an issue by ID.
// Returns storage.ErrNotFound (wrapped) if the issue does not exist.
func (s *DoltStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	var issue *types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		issue, err = issueops.GetIssueInTx(ctx, tx, id)
		return err
	})
	return issue, err
}

// GetIssueByExternalRef retrieves an issue by external reference.
// Returns storage.ErrNotFound (wrapped) if no issue with the given external reference exists.
func (s *DoltStore) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	var id string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = issueops.GetIssueByExternalRefInTx(ctx, tx, externalRef)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// validateUpdateMetadata validates an inbound metadata update value against the
// configured schema (GH#1416 Phase 2) before any wisp routing. It is a no-op
// when the update carries no "metadata" key. Shared by UpdateIssue and
// UpdateIssueChecked so both apply the identical pre-write validation.
func validateUpdateMetadata(updates map[string]interface{}) error {
	rawMeta, ok := updates["metadata"]
	if !ok {
		return nil
	}
	metadataStr, err := storage.NormalizeMetadataValue(rawMeta)
	if err != nil {
		return fmt.Errorf("invalid metadata: %w", err)
	}
	return validateMetadataIfConfigured(json.RawMessage(metadataStr))
}

// checkExpectedVersionInTx enforces the optional ExpectedVersion CAS
// precondition inside tx: when expectedVersion is non-nil the row's current
// RowVersion (row_lock) must still equal it, else the caller's transaction
// returns storage.ErrVersionMismatch and rolls back with the issue unchanged. A
// nil expectedVersion disables the check (an unconditional update).
func checkExpectedVersionInTx(ctx context.Context, tx *sql.Tx, id string, expectedVersion *int64) error {
	if expectedVersion == nil {
		return nil
	}
	return issueops.CheckVersionInTx(ctx, tx, id, *expectedVersion)
}

// UpdateIssue updates fields on an issue.
// Delegates SQL work to issueops.UpdateIssueInTx; handles Dolt-specific concerns
// (metadata validation, DemoteToWisp, DOLT_ADD/COMMIT, cache invalidation).
func (s *DoltStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Validate metadata against schema before wisp routing (GH#1416 Phase 2).
	if err := validateUpdateMetadata(updates); err != nil {
		return err
	}

	// Route ephemeral IDs to wisps table (falls through for promoted wisps).
	// Wisps skip DOLT_COMMIT since they live in dolt_ignored tables.
	if s.isActiveWisp(ctx, id) {
		return s.updateWisp(ctx, id, updates, actor)
	}

	// If updating a regular issue to no-history or ephemeral, migrate it to the
	// wisps table instead of updating in-place. Table routing only happens at
	// create time by default, so we must perform the migration here. (be-x4l)
	_, settingNoHistory := updates["no_history"]
	_, settingWisp := updates["wisp"]
	if settingNoHistory || settingWisp {
		return s.DemoteToWisp(ctx, id, updates, actor)
	}

	// Wrap in withRetryTx so a concurrent writer that loses Dolt's optimistic
	// commit-time merge (MySQL 1213/1205, guaranteed server-side rollback) is
	// retried rather than surfaced as a hard failure. Dolt has no real row
	// locking — FOR UPDATE / SKIP LOCKED are parse-only no-ops
	// (https://www.dolthub.com/blog/2023-10-23-hold-my-beer/) — so retry is the
	// only safety net. withRetryTx owns BeginTx and the final Commit.
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if _, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, actor); err != nil {
			return err
		}

		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: update %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// UpdateIssueChecked applies the update like UpdateIssue, adding an optional
// optimistic-concurrency precondition: when opts.ExpectedVersion is non-nil the
// update proceeds only if the issue's current RowVersion (row_lock) still equals
// *opts.ExpectedVersion, else it refuses with storage.ErrVersionMismatch. The
// version read and the update share ONE transaction, so a mismatch returns
// before any write and the transaction rolls back with the issue unchanged (a
// true compare-and-swap). nil disables the check, leaving behavior identical to
// UpdateIssue. Mirrors UpdateIssue's Dolt-specific concerns (metadata
// validation, wisp routing, DemoteToWisp, DOLT_ADD/COMMIT); UpdateIssue is the
// hot path and is left untouched.
func (s *DoltStore) UpdateIssueChecked(ctx context.Context, id string, updates map[string]interface{}, actor string, opts storage.UpdateIssueOptions) error {
	// Validate metadata against schema before wisp routing (GH#1416 Phase 2).
	if err := validateUpdateMetadata(updates); err != nil {
		return err
	}

	// Route ephemeral IDs to wisps table (falls through for promoted wisps).
	// Wisps skip DOLT_COMMIT since they live in dolt_ignored tables.
	if s.isActiveWisp(ctx, id) {
		return s.updateWispChecked(ctx, id, updates, actor, opts.ExpectedVersion)
	}

	// If updating a regular issue to no-history or ephemeral, migrate it to the
	// wisps table instead of updating in-place (mirrors UpdateIssue). The version
	// check shares the demotion transaction so the CAS stays atomic on this path.
	_, settingNoHistory := updates["no_history"]
	_, settingWisp := updates["wisp"]
	if settingNoHistory || settingWisp {
		return s.withRetryTx(ctx, func(tx *sql.Tx) error {
			if err := checkExpectedVersionInTx(ctx, tx, id, opts.ExpectedVersion); err != nil {
				return err
			}
			return s.demoteToWispInTx(ctx, tx, id, updates, actor)
		})
	}

	// Wrap in withRetryTx exactly like UpdateIssue so a concurrent writer that
	// loses Dolt's optimistic commit-time merge (MySQL 1213/1205, guaranteed
	// server-side rollback) is retried rather than surfaced as a hard failure.
	// A version mismatch (storage.ErrVersionMismatch) is NOT a serialization
	// error, so withRetryTx surfaces it permanently and the transaction rolls
	// back — no update and no event are written (the atomic-refuse property). A
	// concurrent write that commits DURING this tx collides on the row_lock cell
	// and is replayed by withRetryTx, which re-reads the new version here and
	// refuses. withRetryTx owns BeginTx and the final Commit.
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := checkExpectedVersionInTx(ctx, tx, id, opts.ExpectedVersion); err != nil {
			return err
		}
		if _, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, actor); err != nil {
			return err
		}

		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: update %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// ClaimIssue atomically claims an issue using compare-and-swap semantics.
// It sets the assignee to actor and status to "in_progress" only if the issue
// currently has no assignee. Returns storage.ErrAlreadyClaimed if already claimed.
// Delegates SQL work to issueops.ClaimIssueInTx; handles Dolt-specific concerns
// (wisp routing, DOLT_ADD/COMMIT, cache invalidation).
func (s *DoltStore) ClaimIssue(ctx context.Context, id string, actor string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps).
	// Wisps skip DOLT_COMMIT since they live in dolt_ignored tables.
	if s.isActiveWisp(ctx, id) {
		return s.claimWisp(ctx, id, actor)
	}

	// Wrap in withRetryTx so a concurrent claim that loses Dolt's optimistic
	// commit-time merge (MySQL 1213/1205, guaranteed server-side rollback) is
	// retried instead of surfaced as a hard failure. Dolt has no real row
	// locking — FOR UPDATE / SKIP LOCKED are parse-only no-ops
	// (https://www.dolthub.com/blog/2023-10-23-hold-my-beer/) — so retry is the
	// only safety net under concurrent claimants. The body stays a single tx
	// (CAS + DOLT_COMMIT); withRetryTx owns BeginTx and the final Commit.
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if _, err := issueops.ClaimIssueInTx(ctx, tx, id, actor); err != nil {
			return err
		}

		// Dolt versioning for permanent issues.
		// GH#2455: Stage only the tables we modified, then commit without -A.
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: claim %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// ClaimReadyIssue atomically claims the first ready issue matching filter.
func (s *DoltStore) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	// Wrap in withRetryTx: under concurrent workers the loser of Dolt's
	// optimistic commit-time merge gets MySQL 1213/1205 (guaranteed server-side
	// rollback). Retrying re-scans the ready front from a fresh snapshot and
	// claims the next available issue instead of failing the dequeue. Dolt has
	// no real row locking — FOR UPDATE / SKIP LOCKED are parse-only no-ops
	// (https://www.dolthub.com/blog/2023-10-23-hold-my-beer/) — so retry is the
	// safety net. withRetryTx owns BeginTx and the final Commit.
	var claimed *types.Issue
	err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		claimed, err = issueops.ClaimReadyIssueInTx(ctx, tx, filter, actor)
		if err != nil {
			return err
		}
		if claimed == nil {
			return nil
		}

		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: claim ready %s", claimed.ID)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// HeartbeatIssue refreshes the lease on an issue actor holds in_progress,
// pushing lease_expires_at forward on its row in the ephemeral leases table
// (see issueops.lease). Deliberately NO DOLT_ADD/DOLT_COMMIT: the leases
// table is dolt_ignored, so a heartbeat mints no commit and no history — this
// is the whole point of bd-lrgn1 (fleet heartbeats were the dominant source
// of unbounded reachable history). Wrapped in withRetryTx so a heartbeat that
// loses Dolt's optimistic merge to a concurrent reclaim/close on the same
// lease row is replayed against a fresh snapshot rather than surfaced.
func (s *DoltStore) HeartbeatIssue(ctx context.Context, id, actor string) error {
	if s.isActiveWisp(ctx, id) {
		// Wisps are ephemeral and never leased; nothing to heartbeat.
		return fmt.Errorf("%w: %s is ephemeral", storage.ErrNotClaimable, id)
	}
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.HeartbeatIssueInTx(ctx, tx, id, actor)
	})
}

// ReclaimExpiredLeases reverts in_progress issues whose lease expired more than
// olderThan ago back to ready, recovering work stranded by dead workers. The
// reclaim rewrites row_lock so it conflicts with any racing heartbeat/close on
// the same row; withRetryTx replays the loser. Returns the reclaimed issues.
func (s *DoltStore) ReclaimExpiredLeases(ctx context.Context, olderThan time.Duration, actor string) ([]types.ReclaimedLease, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	var reclaimed []types.ReclaimedLease
	err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		reclaimed, err = issueops.ReclaimExpiredLeasesInTx(ctx, tx, cutoff, actor)
		if err != nil {
			return err
		}
		if len(reclaimed) == 0 {
			return nil
		}
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: reclaim %d expired lease(s)", len(reclaimed))
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reclaimed, nil
}

// UnclaimIssue atomically unclaims an issue by clearing the assignee, resetting
// status to "open", deleting its lease row and rewriting row_lock. Records
// an "unclaimed" event. Only the current assignee may release its own claim
// unless force is set (admin/reaper override). Delegates SQL work to
// issueops.UnclaimIssueInTx; handles Dolt-specific concerns (DOLT_ADD/COMMIT).
//
// Wrapped in withRetryTx like the other claim-family writes so a concurrent
// writer that loses Dolt's optimistic commit-time merge (1213/1205) is retried
// rather than surfaced as a hard failure.
func (s *DoltStore) UnclaimIssue(ctx context.Context, id string, actor string, force bool) error {
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.UnclaimIssueInTx(ctx, tx, id, actor, force); err != nil {
			return err
		}

		// Dolt versioning for permanent issues.
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: unclaim %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// UnclaimIssueIfAssignee releases a claim only while the issue is still assigned
// to expectedAssignee (compare-and-swap, the inverse of ClaimIssue). Returns
// storage.ErrAssigneeMismatch, leaving the issue untouched, when the current
// assignee differs. Delegates SQL work to issueops.UnclaimIssueIfAssigneeInTx;
// handles Dolt-specific concerns (DOLT_ADD/COMMIT). Wrapped in withRetryTx like
// UnclaimIssue so a concurrent writer that loses Dolt's optimistic commit-time
// merge is retried rather than surfaced as a hard failure.
func (s *DoltStore) UnclaimIssueIfAssignee(ctx context.Context, id string, actor string, expectedAssignee string) error {
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.UnclaimIssueIfAssigneeInTx(ctx, tx, id, actor, expectedAssignee); err != nil {
			return err
		}

		// Dolt versioning for permanent issues.
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: unclaim %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// ReopenIssue reopens a closed issue, setting status to open and clearing
// closed_at and defer_until. If reason is non-empty, it is recorded as a comment.
// Wraps UpdateIssue for Dolt-specific concerns (wisp routing, DOLT_COMMIT, etc.).
func (s *DoltStore) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
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
// Wraps UpdateIssue for Dolt-specific concerns (wisp routing, DOLT_COMMIT, etc.).
func (s *DoltStore) UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error {
	return s.UpdateIssue(ctx, id, map[string]interface{}{"issue_type": issueType}, actor)
}

// CloseIssue closes an issue with a reason.
// Delegates SQL work to issueops.CloseIssueInTx; handles Dolt-specific concerns
// (wisp routing, DOLT_ADD/COMMIT, cache invalidation).
func (s *DoltStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps).
	// Wisps skip DOLT_COMMIT since they live in dolt_ignored tables.
	if s.isActiveWisp(ctx, id) {
		return s.closeWisp(ctx, id, reason, actor, session)
	}

	// Wrap in withRetryTx so a concurrent writer that loses Dolt's optimistic
	// commit-time merge (MySQL 1213/1205, guaranteed server-side rollback) is
	// retried rather than surfaced as a hard failure. Dolt has no real row
	// locking — FOR UPDATE / SKIP LOCKED are parse-only no-ops
	// (https://www.dolthub.com/blog/2023-10-23-hold-my-beer/) — so retry is the
	// only safety net. withRetryTx owns BeginTx and the final Commit.
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if _, err := issueops.CloseIssueInTx(ctx, tx, id, reason, actor, session); err != nil {
			return err
		}

		// Dolt versioning for permanent issues.
		// GH#2455: Stage only the tables we modified, then commit without -A.
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: close %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// CloseIssueChecked closes an issue but refuses with storage.ErrCloseBlocked
// when it has a live direct blocker unless opts.Force is set, and — when
// opts.ExpectedVersion is non-nil — with storage.ErrVersionMismatch when the
// row's current RowVersion no longer matches (an orthogonal CAS that Force does
// not bypass). Both checks and the close share one transaction, so they are
// atomic (no TOCTOU). Mirrors CloseIssue's Dolt-specific concerns (wisp routing,
// DOLT_ADD/COMMIT).
func (s *DoltStore) CloseIssueChecked(ctx context.Context, id string, actor string, opts storage.CloseIssueOptions) (storage.CloseIssueResult, error) {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps).
	// Wisps skip DOLT_COMMIT since they live in dolt_ignored tables.
	if s.isActiveWisp(ctx, id) {
		return s.closeWispChecked(ctx, id, actor, opts)
	}

	// Wrap in withRetryTx exactly like CloseIssue so a concurrent writer that
	// loses Dolt's optimistic commit-time merge (MySQL 1213/1205, guaranteed
	// server-side rollback) is retried. A blocked-guard rejection
	// (storage.ErrCloseBlocked) is NOT a serialization error, so withRetryTx
	// surfaces it permanently and the transaction rolls back — no close and no
	// event are written (the atomic-refuse property).
	var result storage.CloseIssueResult
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		res, err := issueops.CloseIssueCheckedInTx(ctx, tx, id, opts.Reason, actor, opts.Session, opts.Force, opts.ExpectedVersion)
		if err != nil {
			return err
		}
		result = storage.CloseIssueResult{Unchanged: res.AlreadyClosed}

		// Dolt versioning for permanent issues.
		// GH#2455: Stage only the tables we modified, then commit without -A.
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: close %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	}); err != nil {
		return storage.CloseIssueResult{}, err
	}
	return result, nil
}

// DeleteIssue permanently removes an issue
func (s *DoltStore) DeleteIssue(ctx context.Context, id string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps)
	if s.isActiveWisp(ctx, id) {
		return s.deleteWisp(ctx, id)
	}

	if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.DeleteIssueInTx(ctx, tx, id); err != nil {
			return err
		}

		for _, table := range []string{"issues", "dependencies", "labels", "comments", "events", "child_counters", "issue_snapshots", "compaction_snapshots"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: delete %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// DeleteIssues deletes multiple issues in a single transaction.
// If cascade is true, recursively deletes dependents.
// If cascade is false but force is true, deletes issues and orphans dependents.
// If both are false, returns an error if any issue has dependents.
// If dryRun is true, only computes statistics without deleting.
// deleteBatchSize controls the maximum number of IDs per IN-clause query.
// Kept small to avoid large IN-clause queries. See steveyegge/beads#1692.
const deleteBatchSize = 50

// maxRecursiveResults is the safety limit for the total number of issues discovered
// during recursive dependent traversal. Used by wisps.go.
const maxRecursiveResults = 10000

// queryBatchSize controls the maximum number of IDs per IN-clause in read
// queries (label hydration, wisp lookups). Without batching, queries like
// `SELECT ... FROM wisp_labels WHERE issue_id IN (?,?,?,...thousands)` take
// 20+ seconds on databases with many wisps (e.g., hq with 29K wisps).
const queryBatchSize = 200

func (s *DoltStore) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{}, nil
	}

	// Route wisp IDs to wisp deletion; process regular IDs in batch below.
	// DoltStore uses its own batch wisp deletion (separate transactions per batch
	// to avoid write timeout on large sets — see bd-2ehd, ff-tqm).
	ephIDs, regularIDs := s.partitionByWispStatus(ctx, ids)
	wispDeleteCount := 0
	if len(ephIDs) > 0 {
		var activeWispIDs []string
		for _, eid := range ephIDs {
			if s.isActiveWisp(ctx, eid) {
				activeWispIDs = append(activeWispIDs, eid)
			}
		}
		wispDeleteCount = len(activeWispIDs)
		if !dryRun && len(activeWispIDs) > 0 {
			deleted, err := s.deleteWispBatch(ctx, activeWispIDs)
			if err != nil {
				return nil, fmt.Errorf("failed to batch delete wisps: %w", err)
			}
			wispDeleteCount = deleted
		}
	}
	ids = regularIDs
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{DeletedCount: wispDeleteCount}, nil
	}

	var result *types.DeleteIssuesResult
	if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		r, err := issueops.DeleteIssuesInTx(ctx, tx, ids, cascade, force, dryRun)
		if err != nil {
			result = r
			return err
		}
		result = r
		if dryRun {
			return nil
		}

		for _, table := range []string{"issues", "dependencies", "labels", "comments", "events", "child_counters", "issue_snapshots", "compaction_snapshots"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: delete %d issue(s)", result.DeletedCount)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	}); err != nil {
		// Preserve partial result (e.g., OrphanedIssues) on error.
		if result != nil {
			result.DeletedCount += wispDeleteCount
		}
		return result, err
	}
	result.DeletedCount += wispDeleteCount

	return result, nil
}

// doltBuildSQLInClause builds a parameterized IN clause for SQL queries
func doltBuildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

// =============================================================================
// Helper functions
// =============================================================================

func recordEvent(ctx context.Context, tx *sql.Tx, issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?, ?)
	`, issueops.NewEventID(), issueID, eventType, actor, oldValue, newValue)
	return wrapExecError("record event", err)
}

// seedCounterFromExistingIssuesTx scans existing issues to find the highest numeric suffix
// for the given prefix, then seeds the issue_counter table if no row exists yet.
// This is called when counter mode is first enabled on a repo that already has issues,
// to prevent counter collisions with manually-created sequential IDs (GH#2002).
// It is idempotent: if a counter row already exists for this prefix, it does nothing.
func seedCounterFromExistingIssuesTx(ctx context.Context, tx *sql.Tx, prefix string) error {
	// Check whether a counter row already exists for this prefix.
	// If it does, we must not overwrite it (the counter may already be in use).
	var existing int
	err := tx.QueryRowContext(ctx, "SELECT last_id FROM issue_counter WHERE prefix = ?", prefix).Scan(&existing)
	if err == nil {
		// Row exists - counter is already initialized, nothing to do.
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("failed to check issue_counter for prefix %q: %w", prefix, err)
	}

	// No counter row yet. Scan existing issues to find the highest numeric suffix.
	likePattern := prefix + "-%"
	rows, err := tx.QueryContext(ctx, "SELECT id FROM issues WHERE id LIKE ?", likePattern)
	if err != nil {
		return fmt.Errorf("failed to query existing issues for prefix %q: %w", prefix, err)
	}
	defer rows.Close()

	maxNum := 0
	prefixDash := prefix + "-"
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan issue id: %w", err)
		}
		// Strip the prefix and attempt to parse the remainder as an integer.
		suffix := strings.TrimPrefix(id, prefixDash)
		if suffix == id {
			// id did not start with prefix- (should not happen given LIKE, but be safe)
			continue
		}
		var num int
		if _, parseErr := fmt.Sscanf(suffix, "%d", &num); parseErr == nil && fmt.Sprintf("%d", num) == suffix {
			if num > maxNum {
				maxNum = num
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate existing issues for prefix %q: %w", prefix, err)
	}

	// Only insert a seed row if we found at least one numeric ID.
	// If no numeric IDs exist, the counter will naturally start at 1 on first use.
	if maxNum > 0 {
		_, err = tx.ExecContext(ctx,
			"INSERT INTO issue_counter (prefix, last_id) VALUES (?, ?)",
			prefix, maxNum)
		if err != nil {
			return fmt.Errorf("failed to seed issue_counter for prefix %q at %d: %w", prefix, maxNum, err)
		}
	}

	return nil
}

// nextCounterIDTx atomically increments and returns the next sequential issue ID
// for the given prefix within an existing transaction. Returns the full ID string
// (e.g., "bd-1"). Used by both generateIssueID and generateIssueIDInTable.
func nextCounterIDTx(ctx context.Context, tx *sql.Tx, prefix string) (string, error) {
	// Increment atomically at the DB level to avoid duplicate IDs under
	// concurrent transactions (GH#2002). "last_id = last_id + 1" is evaluated
	// by the DB engine atomically within Dolt's MVCC.

	// Attempt atomic increment of an existing counter row.
	res, err := tx.ExecContext(ctx, "UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?", prefix)
	if err != nil {
		return "", fmt.Errorf("failed to increment issue counter for prefix %q: %w", prefix, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("failed to check rows affected for issue counter prefix %q: %w", prefix, err)
	}

	if rowsAffected == 0 {
		// No counter row yet - seed from existing issues before proceeding to
		// avoid collisions with manually-created sequential IDs (GH#2002).
		if seedErr := seedCounterFromExistingIssuesTx(ctx, tx, prefix); seedErr != nil {
			return "", fmt.Errorf("failed to seed issue counter for prefix %q: %w", prefix, seedErr)
		}
		// Retry the atomic increment after seeding.
		res, err = tx.ExecContext(ctx, "UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?", prefix)
		if err != nil {
			return "", fmt.Errorf("failed to increment issue counter after seeding for prefix %q: %w", prefix, err)
		}
		rowsAffected, err = res.RowsAffected()
		if err != nil {
			return "", fmt.Errorf("failed to check rows affected after seeding for prefix %q: %w", prefix, err)
		}
		if rowsAffected == 0 {
			// Seeding found no existing numeric IDs -- insert the initial row.
			_, err = tx.ExecContext(ctx, "INSERT INTO issue_counter (prefix, last_id) VALUES (?, 1)", prefix)
			if err != nil {
				return "", fmt.Errorf("failed to insert initial issue counter for prefix %q: %w", prefix, err)
			}
		}
	}

	// Read back the value that was atomically set by the DB engine.
	var nextID int
	err = tx.QueryRowContext(ctx, "SELECT last_id FROM issue_counter WHERE prefix = ?", prefix).Scan(&nextID)
	if err != nil {
		return "", fmt.Errorf("failed to read issue counter after increment for prefix %q: %w", prefix, err)
	}
	return fmt.Sprintf("%s-%d", prefix, nextID), nil
}

// isCounterModeTx checks whether issue_id_mode=counter is configured.
func isCounterModeTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var idMode string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_id_mode").Scan(&idMode)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("failed to read issue_id_mode config: %w", err)
	}
	return idMode == "counter", nil
}

// generateHashID creates a hash-based ID for a top-level issue.
// Uses base36 encoding (0-9, a-z) for better information density than hex.
func generateHashID(prefix, title, description, creator string, timestamp time.Time, length, nonce int) string {
	return idgen.GenerateHashID(prefix, title, description, creator, timestamp, length, nonce)
}

// Thin wrappers around exported issueops functions, kept for internal callers.
var (
	isAllowedUpdateField = issueops.IsAllowedUpdateField
	manageClosedAt       = issueops.ManageClosedAt
	determineEventType   = issueops.DetermineEventType
)

// Aliases for shared nullable helpers from issueops.
var (
	nullString    = issueops.NullString
	nullStringPtr = issueops.NullStringPtr
	nullInt       = issueops.NullInt
	nullIntVal    = issueops.NullIntVal
)

// Aliases for shared helpers from issueops.
var (
	jsonMetadata          = issueops.JSONMetadata
	parseJSONStringArray  = issueops.ParseJSONStringArray
	formatJSONStringArray = issueops.FormatJSONStringArray
)

// DeleteIssuesBySourceRepo permanently removes all issues from a specific source repository.
// This is used when a repo is removed from the multi-repo configuration.
// It also cleans up related data: dependencies, labels, comments, and events.
// Returns the number of issues deleted.
func (s *DoltStore) DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error) {
	var count int
	err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		count, err = issueops.DeleteIssuesBySourceRepoInTx(ctx, tx, sourceRepo)
		return err
	})
	return count, err
}

// ClearRepoMtime removes the mtime cache entry for a repository.
func (s *DoltStore) ClearRepoMtime(ctx context.Context, repoPath string) error {
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.ClearRepoMtimeInTx(ctx, tx, repoPath)
	})
}

// GetRepoMtime returns the cached mtime (in nanoseconds) for a repository's data file.
// Returns 0 if no cache entry exists.
func (s *DoltStore) GetRepoMtime(ctx context.Context, repoPath string) (int64, error) {
	var result int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetRepoMtimeInTx(ctx, tx, repoPath)
		return err
	})
	return result, err
}

// SetRepoMtime updates the mtime cache for a repository's data file.
func (s *DoltStore) SetRepoMtime(ctx context.Context, repoPath, jsonlPath string, mtimeNs int64) error {
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.SetRepoMtimeInTx(ctx, tx, repoPath, jsonlPath, mtimeNs)
	})
}
