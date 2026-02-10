//go:build cgo

package dolt

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// UpdateIssueID updates an issue ID and all its references.
// Disables FK checks to allow updating the primary key in issues while
// child tables (dependencies, etc.) still reference the old ID.
func (s *DoltStore) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Disable foreign key checks to allow PK update on issues table
	// (child tables like dependencies, events, etc. reference issues.id)
	_, err = tx.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 0`)
	if err != nil {
		return fmt.Errorf("failed to disable foreign key checks: %w", err)
	}

	// Update the issue itself
	result, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET id = ?, title = ?, description = ?, design = ?, acceptance_criteria = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`, newID, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes, time.Now().UTC(), oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue ID: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", oldID)
	}

	// Update references in dependencies
	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in dependencies: %w", err)
	}

	// Update references in events
	_, err = tx.ExecContext(ctx, `UPDATE events SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update events: %w", err)
	}

	// Update references in labels
	_, err = tx.ExecContext(ctx, `UPDATE labels SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}

	// Update references in comments
	_, err = tx.ExecContext(ctx, `UPDATE comments SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update comments: %w", err)
	}

	// Update references in issue_snapshots
	_, err = tx.ExecContext(ctx, `UPDATE issue_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_snapshots: %w", err)
	}

	// Update references in compaction_snapshots
	_, err = tx.ExecContext(ctx, `UPDATE compaction_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update compaction_snapshots: %w", err)
	}

	// Update references in export_hashes
	_, err = tx.ExecContext(ctx, `UPDATE export_hashes SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update export_hashes: %w", err)
	}

	// Update references in child_counters
	_, err = tx.ExecContext(ctx, `UPDATE child_counters SET parent_id = ? WHERE parent_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update child_counters: %w", err)
	}

	// Record rename event
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	if err != nil {
		return fmt.Errorf("failed to record rename event: %w", err)
	}

	// Re-enable foreign key checks before commit
	_, err = tx.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 1`)
	if err != nil {
		return fmt.Errorf("failed to re-enable foreign key checks: %w", err)
	}

	return tx.Commit()
}

// RenameDependencyPrefix updates the prefix in all dependency records
func (s *DoltStore) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Update issue_id column
	_, err = tx.ExecContext(ctx, `
		UPDATE dependencies
		SET issue_id = CONCAT(?, SUBSTRING(issue_id, LENGTH(?) + 1))
		WHERE issue_id LIKE CONCAT(?, '%')
	`, newPrefix, oldPrefix, oldPrefix)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in dependencies: %w", err)
	}

	// Update depends_on_id column
	_, err = tx.ExecContext(ctx, `
		UPDATE dependencies
		SET depends_on_id = CONCAT(?, SUBSTRING(depends_on_id, LENGTH(?) + 1))
		WHERE depends_on_id LIKE CONCAT(?, '%')
	`, newPrefix, oldPrefix, oldPrefix)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in dependencies: %w", err)
	}

	return tx.Commit()
}

// RenameCounterPrefix is a no-op with hash-based IDs
func (s *DoltStore) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	// Hash-based IDs don't use counters
	return nil
}
