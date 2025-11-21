package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// validateBatchIssues validates all issues in a batch and sets timestamps if not provided
func validateBatchIssues(issues []*types.Issue) error {
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

		if err := issue.Validate(); err != nil {
			return fmt.Errorf("validation failed for issue %d: %w", i, err)
		}
	}
	return nil
}

// generateBatchIDs generates IDs for all issues that need them atomically
func (s *SQLiteStorage) generateBatchIDs(ctx context.Context, conn *sql.Conn, issues []*types.Issue, actor string, orphanHandling OrphanHandling) error {
	// Get prefix from config (needed for both generation and validation)
	var prefix string
	err := conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing (bd-166)
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Generate or validate IDs for all issues
	if err := EnsureIDs(ctx, conn, prefix, issues, actor, orphanHandling); err != nil {
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
// REMOVED (bd-c7af): SyncAllCounters example - no longer needed with hash IDs
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

// CreateIssuesWithOptions creates multiple issues with configurable orphan handling
func (s *SQLiteStorage) CreateIssuesWithOptions(ctx context.Context, issues []*types.Issue, actor string, orphanHandling OrphanHandling) error {
	if len(issues) == 0 {
		return nil
	}

	// Phase 1: Validate all issues first (fail-fast)
	if err := validateBatchIssues(issues); err != nil {
		return err
	}

	// Phase 2: Acquire connection and start transaction
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("failed to begin immediate transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Phase 3: Generate IDs for issues that need them
	if err := s.generateBatchIDs(ctx, conn, issues, actor, orphanHandling); err != nil {
		return wrapDBError("generate batch IDs", err)
	}

	// Phase 4: Bulk insert issues
	if err := bulkInsertIssues(ctx, conn, issues); err != nil {
		return wrapDBError("bulk insert issues", err)
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
