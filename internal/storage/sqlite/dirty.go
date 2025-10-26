// Package sqlite implements dirty issue tracking for incremental JSONL export.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// MarkIssueDirty marks an issue as dirty (needs to be exported to JSONL)
// This should be called whenever an issue is created, updated, or has dependencies changed
func (s *SQLiteStorage) MarkIssueDirty(ctx context.Context, issueID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, issueID, time.Now())
	return err
}

// MarkIssuesDirty marks multiple issues as dirty in a single transaction
// More efficient when marking multiple issues (e.g., both sides of a dependency)
func (s *SQLiteStorage) MarkIssuesDirty(ctx context.Context, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issueID := range issueIDs {
		if _, err := stmt.ExecContext(ctx, issueID, now); err != nil {
			return fmt.Errorf("failed to mark issue %s dirty: %w", issueID, err)
		}
	}

	return tx.Commit()
}

// GetDirtyIssues returns the list of issue IDs that need to be exported
func (s *SQLiteStorage) GetDirtyIssues(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id FROM dirty_issues
		ORDER BY marked_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get dirty issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var issueIDs []string
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			return nil, fmt.Errorf("failed to scan issue ID: %w", err)
		}
		issueIDs = append(issueIDs, issueID)
	}

	return issueIDs, rows.Err()
}

// ClearDirtyIssues removes all entries from the dirty_issues table
// This should be called after a successful JSONL export
//
// WARNING: This has a race condition (bd-52). Use ClearDirtyIssuesByID instead
// to only clear specific issues that were actually exported.
func (s *SQLiteStorage) ClearDirtyIssues(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dirty_issues`)
	if err != nil {
		return fmt.Errorf("failed to clear dirty issues: %w", err)
	}
	return nil
}

// ClearDirtyIssuesByID removes specific issue IDs from the dirty_issues table
// This avoids race conditions by only clearing issues that were actually exported
func (s *SQLiteStorage) ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `DELETE FROM dirty_issues WHERE issue_id = ?`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issueID := range issueIDs {
		if _, err := stmt.ExecContext(ctx, issueID); err != nil {
			return fmt.Errorf("failed to clear dirty issue %s: %w", issueID, err)
		}
	}

	return tx.Commit()
}

// GetDirtyIssueCount returns the count of dirty issues (for monitoring/debugging)
func (s *SQLiteStorage) GetDirtyIssueCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dirty_issues`).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to count dirty issues: %w", err)
	}
	return count, nil
}

// markIssuesDirtyTx marks multiple issues as dirty within an existing transaction
// This is a helper for operations that need to mark issues dirty as part of a larger transaction
func markIssuesDirtyTx(ctx context.Context, tx *sql.Tx, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}

	now := time.Now()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare dirty statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issueID := range issueIDs {
		if _, err := stmt.ExecContext(ctx, issueID, now); err != nil {
			return fmt.Errorf("failed to mark issue %s dirty: %w", issueID, err)
		}
	}

	return nil
}
