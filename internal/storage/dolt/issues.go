package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

	return s.withSerializedWrite(ctx, func() error {
		if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
			// SkipPrefixValidation matches legacy behavior: single-issue path does
			// not validate prefixes for explicit IDs.
			bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{
				SkipPrefixValidation: true,
			})
			if err != nil {
				return err
			}
			return issueops.CreateIssueInTx(ctx, tx, bc, issue, actor)
		}); err != nil {
			return err
		}

		// Dolt versioning — wisps and no-history issues skip DOLT_COMMIT.
		if !issue.Ephemeral && !issue.NoHistory {
			if err := s.doltAddAndCommit(ctx, []string{"issues", "events"},
				fmt.Sprintf("bd: create %s", issue.ID)); err != nil {
				return err
			}
		}
		return nil
	})
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

	return s.withSerializedWrite(ctx, func() error {
		// All-wisps fast path: individual transactions, no Dolt versioning.
		// Covers both ephemeral issues and no-history issues (both skip DOLT_COMMIT).
		if issueops.AllWisps(issues) {
			for _, issue := range issues {
				if !issue.NoHistory {
					issue.Ephemeral = true
				}
				if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
					bc, err := issueops.NewBatchContext(ctx, tx, opts)
					if err != nil {
						return err
					}
					return issueops.CreateIssueInTx(ctx, tx, bc, issue, actor)
				}); err != nil {
					return err
				}
			}
			return nil
		}

		if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
			return issueops.CreateIssuesInTx(ctx, tx, issues, actor, opts)
		}); err != nil {
			return err
		}

		// GH#2455: Stage only the tables we modified, then commit without -A.
		return s.doltAddAndCommit(ctx,
			[]string{"issues", "events", "labels", "comments", "dependencies", "child_counters"},
			fmt.Sprintf("bd: create %d issue(s)", len(issues)))
	})
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

// UpdateIssue updates fields on an issue.
// Delegates SQL work to issueops.UpdateIssueInTx; handles Dolt-specific concerns
// (metadata validation, DemoteToWisp, DOLT_ADD/COMMIT, cache invalidation).
func (s *DoltStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Validate metadata against schema before wisp routing (GH#1416 Phase 2)
	if rawMeta, ok := updates["metadata"]; ok {
		metadataStr, err := storage.NormalizeMetadataValue(rawMeta)
		if err != nil {
			return fmt.Errorf("invalid metadata: %w", err)
		}
		if err := validateMetadataIfConfigured(json.RawMessage(metadataStr)); err != nil {
			return err
		}
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

	return s.withSerializedWrite(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		result, err := issueops.UpdateIssueInTx(ctx, tx, id, updates, actor)
		if err != nil {
			return err
		}

		// Dolt versioning for permanent issues.
		// GH#2455: Stage only the tables we modified, then commit without -A.
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: update %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return wrapTransactionError("commit update issue", err)
		}
		// Status changes affect the active set used by blocked ID computation
		if _, hasStatus := updates["status"]; hasStatus {
			s.invalidateBlockedIDsCache()
		}
		_ = result // OldIssue available if needed for future cache invalidation
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

	return s.withSerializedWrite(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

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

		if err := tx.Commit(); err != nil {
			return wrapTransactionError("commit claim issue", err)
		}
		// Claiming changes status to in_progress, affecting blocked ID computation
		s.invalidateBlockedIDsCache()
		return nil
	})
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

	return s.withSerializedWrite(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

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

		if err := tx.Commit(); err != nil {
			return wrapTransactionError("commit close issue", err)
		}
		// Closing changes the active set, which affects blocked ID computation (GH#1495)
		s.invalidateBlockedIDsCache()
		return nil
	})
}

// DeleteIssue permanently removes an issue
func (s *DoltStore) DeleteIssue(ctx context.Context, id string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps)
	if s.isActiveWisp(ctx, id) {
		return s.deleteWisp(ctx, id)
	}

	return s.withSerializedWrite(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }() // No-op after successful commit

		if err := issueops.DeleteIssueInTx(ctx, tx, id); err != nil {
			return err
		}

		// GH#2455: Stage only the tables we modified, then commit without -A.
		for _, table := range []string{"issues", "dependencies", "labels", "comments", "events", "child_counters", "issue_snapshots", "compaction_snapshots"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: delete %s", id)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return err
		}
		s.invalidateBlockedIDsCache()
		return nil
	})
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delegate core logic (cascade/force/dryRun, stats, batch deletion) to issueops.
	result, err := issueops.DeleteIssuesInTx(ctx, tx, ids, cascade, force, dryRun)
	if err != nil {
		// Preserve partial result (e.g., OrphanedIssues) on error.
		if result != nil {
			result.DeletedCount += wispDeleteCount
		}
		return result, err
	}
	result.DeletedCount += wispDeleteCount

	if dryRun {
		return result, nil
	}

	// GH#2455: Stage only the tables this operation modified, then commit
	// without -A.
	for _, table := range []string{"issues", "dependencies", "labels", "comments", "events", "child_counters", "issue_snapshots", "compaction_snapshots"} {
		_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
	}
	commitMsg := fmt.Sprintf("bd: delete %d issue(s)", result.DeletedCount-wispDeleteCount)
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
		commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
		return nil, fmt.Errorf("dolt commit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	s.invalidateBlockedIDsCache()
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
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, issueID, eventType, actor, oldValue, newValue)
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
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		count, err = issueops.DeleteIssuesBySourceRepoInTx(ctx, tx, sourceRepo)
		return err
	})
	if err == nil {
		s.invalidateBlockedIDsCache()
	}
	return count, err
}

// ClearRepoMtime removes the mtime cache entry for a repository.
func (s *DoltStore) ClearRepoMtime(ctx context.Context, repoPath string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
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
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.SetRepoMtimeInTx(ctx, tx, repoPath, jsonlPath, mtimeNs)
	})
}
