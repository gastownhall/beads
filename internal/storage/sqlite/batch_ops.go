package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// validateBatchIssues validates all issues in a batch and sets timestamps if not provided
// Uses built-in statuses only for backward compatibility.
func validateBatchIssues(issues []*types.Issue) error {
	return validateBatchIssuesWithCustomStatuses(issues, nil)
}

// validateBatchIssuesWithCustomStatuses validates all issues in a batch,
// allowing custom statuses in addition to built-in ones.
func validateBatchIssuesWithCustomStatuses(issues []*types.Issue, customStatuses []string) error {
	now := time.Now()
	for i, issue := range issues {
		if issue == nil {
			return fmt.Errorf("issue %d is nil", i)
		}

		// Only set timestamps if not already provided
		if issue.CreatedAt.IsZero() {
			issue.CreatedAt = now
		}
		if issue.UpdatedAt.IsZero() {
			issue.UpdatedAt = now
		}

		// Defensive fix for closed_at invariant (GH#523): older versions of bd could
		// close issues without setting closed_at. Fix by using max(created_at, updated_at) + 1s.
		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			maxTime := issue.CreatedAt
			if issue.UpdatedAt.After(maxTime) {
				maxTime = issue.UpdatedAt
			}
			closedAt := maxTime.Add(time.Second)
			issue.ClosedAt = &closedAt
		}

		// Defensive fix for deleted_at invariant: tombstones must have deleted_at
		if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
			maxTime := issue.CreatedAt
			if issue.UpdatedAt.After(maxTime) {
				maxTime = issue.UpdatedAt
			}
			deletedAt := maxTime.Add(time.Second)
			issue.DeletedAt = &deletedAt
		}

		if err := issue.ValidateWithCustomStatuses(customStatuses); err != nil {
			return fmt.Errorf("validation failed for issue %d: %w", i, err)
		}
	}
	return nil
}

// generateBatchIDs generates IDs for all issues that need them atomically
func (s *SQLiteStorage) generateBatchIDs(ctx context.Context, conn *sql.Conn, issues []*types.Issue, actor string, orphanHandling OrphanHandling, skipPrefixValidation bool) error {
	// Get prefix from config (needed for both generation and validation)
	var prefix string
	err := conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Generate or validate IDs for all issues
	if err := EnsureIDs(ctx, conn, prefix, issues, actor, orphanHandling, skipPrefixValidation); err != nil {
		return wrapDBError("ensure IDs", err)
	}
	
	// Compute content hashes
	for i := range issues {
		if issues[i].ContentHash == "" {
			issues[i].ContentHash = issues[i].ComputeContentHash()
		}
	}
	return nil
}

// bulkInsertIssues delegates to insertIssues helper
func bulkInsertIssues(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	return insertIssues(ctx, conn, issues)
}

// bulkRecordEvents delegates to recordCreatedEvents helper
func bulkRecordEvents(ctx context.Context, conn *sql.Conn, issues []*types.Issue, actor string) error {
	return recordCreatedEvents(ctx, conn, issues, actor)
}

// bulkMarkDirty delegates to markDirtyBatch helper
func bulkMarkDirty(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	return markDirtyBatch(ctx, conn, issues)
}

// updateChildCountersForHierarchicalIDs updates child_counters for all hierarchical IDs in the batch.
// This is called AFTER issues are inserted so that parents exist for the foreign key constraint.
// (GH#728 fix)
func updateChildCountersForHierarchicalIDs(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	for _, issue := range issues {
		if issue.ID == "" {
			continue // Skip issues that were filtered out (e.g., OrphanSkip)
		}
		if parentID, childNum, ok := ParseHierarchicalID(issue.ID); ok {
			// Only update if parent exists (it should after insert, but check to be safe)
			var parentCount int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, parentID).Scan(&parentCount); err != nil {
				return fmt.Errorf("failed to check parent existence for %s: %w", parentID, err)
			}
			if parentCount > 0 {
				if err := ensureChildCounterUpdatedWithConn(ctx, conn, parentID, childNum); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkForExistingIDs verifies that:
// 1. There are no duplicate IDs within the batch itself
// 2. None of the issue IDs already exist in the database
// Returns an error if any conflicts are found, ensuring CreateIssues fails atomically
// rather than silently skipping duplicates via INSERT OR IGNORE.
func checkForExistingIDs(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	if len(issues) == 0 {
		return nil
	}

	// Build list of IDs to check and detect duplicates within batch
	seenIDs := make(map[string]bool)
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.ID != "" {
			// Check for duplicates within the batch
			if seenIDs[issue.ID] {
				return fmt.Errorf("duplicate issue ID within batch: %s", issue.ID)
			}
			seenIDs[issue.ID] = true
			ids = append(ids, issue.ID)
		}
	}

	if len(ids) == 0 {
		return nil
	}

	// Check for existing IDs in database using a single query with IN clause
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf("SELECT id FROM issues WHERE id IN (%s) LIMIT 1", strings.Join(placeholders, ","))
	var existingID string
	err := conn.QueryRowContext(ctx, query, args...).Scan(&existingID)
	if err == nil {
		// Found an existing ID
		return fmt.Errorf("issue ID %s already exists", existingID)
	}
	if err != sql.ErrNoRows {
		// Unexpected error
		return fmt.Errorf("failed to check for existing IDs: %w", err)
	}

	return nil
}

// CreateIssues creates multiple issues atomically in a single transaction.
// This provides significant performance improvements over calling CreateIssue in a loop:
// - Single connection acquisition
// - Single transaction
// - Atomic ID range reservation (one counter update for N issues)
// - All-or-nothing atomicity
//
// Expected 5-10x speedup for batches of 10+ issues.
// CreateIssues creates multiple issues atomically in a single transaction.
//
// This method is optimized for bulk issue creation and provides significant
// performance improvements over calling CreateIssue in a loop:
//   - Single database connection and transaction
//   - Atomic ID range reservation (one counter update for N IDs)
//   - All-or-nothing semantics (rolls back on any error)
//   - 5-15x faster than sequential CreateIssue calls
//
// All issues are validated before any database changes occur. If any issue
// fails validation, the entire batch is rejected.
//
// ID Assignment:
//   - Issues with empty ID get auto-generated IDs from a reserved range
//   - Issues with explicit IDs use those IDs (caller must ensure uniqueness)
//   - Mix of explicit and auto-generated IDs is supported
//
// Timestamps:
//   - All issues in the batch receive identical created_at/updated_at timestamps
//   - This reflects that they were created as a single atomic operation
//
// Usage:
//   // Bulk import from external source
//   issues := []*types.Issue{...}
//   if err := store.CreateIssues(ctx, issues, "import"); err != nil {
//       return err
//   }
//
//   // After importing with explicit IDs, sync counters to prevent collisions
// REMOVED: SyncAllCounters example - no longer needed with hash IDs
//
// Performance:
//   - 100 issues: ~30ms (vs ~900ms with CreateIssue loop)
//   - 1000 issues: ~950ms (vs estimated 9s with CreateIssue loop)
//
// When to use:
//   - Bulk imports from external systems (use CreateIssues)
//   - Creating multiple related issues at once (use CreateIssues)
//   - Single issue creation (use CreateIssue for simplicity)
//   - Interactive user operations (use CreateIssue)
func (s *SQLiteStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	// Default to OrphanResurrect for backward compatibility
	return s.CreateIssuesWithOptions(ctx, issues, actor, OrphanResurrect)
}

// BatchCreateOptions contains options for batch issue creation
type BatchCreateOptions struct {
	OrphanHandling       OrphanHandling // How to handle missing parent issues
	SkipPrefixValidation bool           // Skip prefix validation for existing IDs (used during import)
}

// CreateIssuesWithOptions creates multiple issues with configurable orphan handling
func (s *SQLiteStorage) CreateIssuesWithOptions(ctx context.Context, issues []*types.Issue, actor string, orphanHandling OrphanHandling) error {
	return s.CreateIssuesWithFullOptions(ctx, issues, actor, BatchCreateOptions{
		OrphanHandling:       orphanHandling,
		SkipPrefixValidation: false,
	})
}

// CreateIssuesWithFullOptions creates multiple issues with full options control
func (s *SQLiteStorage) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts BatchCreateOptions) error {
	if len(issues) == 0 {
		return nil
	}

	// Fetch custom statuses for validation
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}

	// Phase 1: Validate all issues first (fail-fast, with custom status support)
	if err := validateBatchIssuesWithCustomStatuses(issues, customStatuses); err != nil {
		return err
	}

	// Phase 2: Acquire connection and start transaction
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Use retry logic with exponential backoff to handle SQLITE_BUSY under concurrent load
	if err := beginImmediateWithRetry(ctx, conn, 5, 10*time.Millisecond); err != nil {
		return fmt.Errorf("failed to begin immediate transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Phase 3: Generate IDs for issues that need them
	if err := s.generateBatchIDs(ctx, conn, issues, actor, opts.OrphanHandling, opts.SkipPrefixValidation); err != nil {
		return wrapDBError("generate batch IDs", err)
	}

	// Phase 3.5: Check for conflicts with existing IDs in database
	if err := checkForExistingIDs(ctx, conn, issues); err != nil {
		return err
	}

	// Phase 4: Bulk insert issues
	if err := bulkInsertIssues(ctx, conn, issues); err != nil {
		return wrapDBError("bulk insert issues", err)
	}

	// Phase 4.5: Update child counters for hierarchical IDs (GH#728 fix)
	// This must happen AFTER insert so parents exist for the foreign key constraint
	if err := updateChildCountersForHierarchicalIDs(ctx, conn, issues); err != nil {
		return wrapDBError("update child counters", err)
	}

	// Phase 5: Record creation events
	if err := bulkRecordEvents(ctx, conn, issues, actor); err != nil {
		return wrapDBError("record creation events", err)
	}

	// Phase 6: Mark issues dirty for incremental export
	if err := bulkMarkDirty(ctx, conn, issues); err != nil {
		return wrapDBError("mark issues dirty", err)
	}

	// Phase 7: Commit transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	return nil
}
