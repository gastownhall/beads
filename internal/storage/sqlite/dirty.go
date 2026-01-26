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
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, issueID, time.Now())
	return wrapDBErrorf(err, "mark issue %s dirty", issueID)
}

// MarkIssuesDirty marks multiple issues as dirty in a single transaction
// More efficient when marking multiple issues (e.g., both sides of a dependency)
func (s *SQLiteStorage) MarkIssuesDirty(ctx context.Context, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}

	return s.withTx(ctx, func(conn *sql.Conn) error {
		now := time.Now()
		stmt, err := conn.PrepareContext(ctx, `
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

		return nil
	})
}

// GetDirtyIssues returns the list of issue IDs that need to be exported
func (s *SQLiteStorage) GetDirtyIssues(ctx context.Context) ([]string, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

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

	if err := rows.Err(); err != nil {
		return nil, wrapDBError("iterate dirty issues", err)
	}
	return issueIDs, nil
}

// GetDirtyIssueHash returns the stored content hash for a dirty issue, if it exists
func (s *SQLiteStorage) GetDirtyIssueHash(ctx context.Context, issueID string) (string, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var hash sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT content_hash FROM dirty_issues WHERE issue_id = ?
	`, issueID).Scan(&hash)

	if IsNotFound(wrapDBErrorf(err, "get dirty issue hash for %s", issueID)) {
		return "", nil // Issue not dirty
	}
	if err != nil {
		return "", wrapDBErrorf(err, "get dirty issue hash for %s", issueID)
	}

	if !hash.Valid {
		return "", nil // No hash stored yet
	}

	return hash.String, nil
}

// ClearDirtyIssuesByID removes specific issue IDs from the dirty_issues table
// This avoids race conditions by only clearing issues that were actually exported
func (s *SQLiteStorage) ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}

	return s.withTx(ctx, func(conn *sql.Conn) error {
		stmt, err := conn.PrepareContext(ctx, `DELETE FROM dirty_issues WHERE issue_id = ?`)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, issueID := range issueIDs {
			if _, err := stmt.ExecContext(ctx, issueID); err != nil {
				return fmt.Errorf("failed to clear dirty issue %s: %w", issueID, err)
			}
		}

		return nil
	})
}

// GetDirtyIssueCount returns the count of dirty issues (for monitoring/debugging)
func (s *SQLiteStorage) GetDirtyIssueCount(ctx context.Context) (int, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dirty_issues`).Scan(&count)
	if IsNotFound(wrapDBError("count dirty issues", err)) {
		return 0, nil
	}
	if err != nil {
		return 0, wrapDBError("count dirty issues", err)
	}
	return count, nil
}

// markIssuesDirtyTx marks multiple issues as dirty within an existing transaction.
// This is a helper for operations that need to mark issues dirty as part of a larger transaction.
// The exec parameter can be either *sql.Tx or *sql.Conn.
func markIssuesDirtyTx(ctx context.Context, exec dbExecutor, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}

	now := time.Now()
	stmt, err := exec.PrepareContext(ctx, `
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
