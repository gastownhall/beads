package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetExportHash retrieves the content hash of the last export for an issue.
// Returns empty string if no hash is stored (first export).
func (s *SQLiteStorage) GetExportHash(ctx context.Context, issueID string) (string, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT content_hash FROM export_hashes WHERE issue_id = ?
	`, issueID).Scan(&hash)
	
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // No hash stored yet
	}
	if err != nil {
		return "", fmt.Errorf("failed to get export hash for %s: %w", issueID, err)
	}
	
	return hash, nil
}

// SetExportHash stores the content hash of an issue after successful export.
func (s *SQLiteStorage) SetExportHash(ctx context.Context, issueID, contentHash string) error {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO export_hashes (issue_id, content_hash, exported_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(issue_id) DO UPDATE SET
			content_hash = excluded.content_hash,
			exported_at = CURRENT_TIMESTAMP
	`, issueID, contentHash)
	
	if err != nil {
		return fmt.Errorf("failed to set export hash for %s: %w", issueID, err)
	}
	
	return nil
}

// ClearAllExportHashes removes all export hashes from the database.
// This is primarily used for test isolation to force re-export of issues.
func (s *SQLiteStorage) ClearAllExportHashes(ctx context.Context) error {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM export_hashes`)
	if err != nil {
		return fmt.Errorf("failed to clear export hashes: %w", err)
	}
	return nil
}

// GetJSONLFileHash retrieves the stored hash of the JSONL file.
// Returns empty string if no hash is stored (bd-160).
func (s *SQLiteStorage) GetJSONLFileHash(ctx context.Context) (string, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT value FROM metadata WHERE key = 'jsonl_file_hash'
	`).Scan(&hash)
	
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // No hash stored yet
	}
	if err != nil {
		return "", fmt.Errorf("failed to get jsonl_file_hash: %w", err)
	}
	
	return hash, nil
}

// SetJSONLFileHash stores the hash of the JSONL file after export (bd-160).
func (s *SQLiteStorage) SetJSONLFileHash(ctx context.Context, fileHash string) error {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata (key, value)
		VALUES ('jsonl_file_hash', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, fileHash)
	
	if err != nil {
		return fmt.Errorf("failed to set jsonl_file_hash: %w", err)
	}
	
	return nil
}
