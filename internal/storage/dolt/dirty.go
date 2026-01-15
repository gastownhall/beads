package dolt

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// GetDirtyIssues returns IDs of issues that have been modified since last export
func (s *DoltStore) GetDirtyIssues(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT issue_id FROM dirty_issues ORDER BY marked_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get dirty issues: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan issue id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetDirtyIssueHash returns the dirty hash for a specific issue
func (s *DoltStore) GetDirtyIssueHash(ctx context.Context, issueID string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT i.content_hash FROM issues i
		JOIN dirty_issues d ON i.id = d.issue_id
		WHERE d.issue_id = ?
	`, issueID).Scan(&hash)
	if err != nil {
		return "", fmt.Errorf("failed to get dirty issue hash: %w", err)
	}
	return hash, nil
}

// ClearDirtyIssuesByID removes specific issues from the dirty list
func (s *DoltStore) ClearDirtyIssuesByID(ctx context.Context, issueIDs []string) error {
	if len(issueIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// nolint:gosec // G201: placeholders contains only ? markers, actual values passed via args
	query := fmt.Sprintf("DELETE FROM dirty_issues WHERE issue_id IN (%s)", strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to clear dirty issues: %w", err)
	}
	return nil
}

// GetExportHash returns the last export hash for an issue
func (s *DoltStore) GetExportHash(ctx context.Context, issueID string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT content_hash FROM export_hashes WHERE issue_id = ?
	`, issueID).Scan(&hash)
	if err != nil {
		return "", nil // Not found is OK
	}
	return hash, nil
}

// SetExportHash stores the export hash for an issue
func (s *DoltStore) SetExportHash(ctx context.Context, issueID, contentHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO export_hashes (issue_id, content_hash, exported_at)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE content_hash = VALUES(content_hash), exported_at = VALUES(exported_at)
	`, issueID, contentHash, time.Now())
	if err != nil {
		return fmt.Errorf("failed to set export hash: %w", err)
	}
	return nil
}

// ClearAllExportHashes removes all export hashes (for full re-export)
func (s *DoltStore) ClearAllExportHashes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM export_hashes")
	if err != nil {
		return fmt.Errorf("failed to clear export hashes: %w", err)
	}
	return nil
}

// GetJSONLFileHash returns the stored JSONL file hash
func (s *DoltStore) GetJSONLFileHash(ctx context.Context) (string, error) {
	return s.GetMetadata(ctx, "jsonl_file_hash")
}

// SetJSONLFileHash stores the JSONL file hash
func (s *DoltStore) SetJSONLFileHash(ctx context.Context, fileHash string) error {
	return s.SetMetadata(ctx, "jsonl_file_hash", fileHash)
}
