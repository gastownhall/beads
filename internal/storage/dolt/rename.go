package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// UpdateIssueID updates an issue ID and all its references.
// Handles both regular issues (issues + auxiliary tables) and wisps
// (wisps + wisp_* auxiliary tables).
// Disables FK checks to allow updating the primary key while
// child tables still reference the old ID.
func (s *DoltStore) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	// Determine whether the old ID lives in the wisps table or issues table.
	isWisp := s.isActiveWisp(ctx, oldID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 0`)
	if err != nil {
		return fmt.Errorf("failed to disable foreign key checks: %w", err)
	}
	// SET is session-level, not rolled back by tx.Rollback(). Ensure FK checks
	// are re-enabled on the connection even if we return early on error.
	defer func() { _, _ = tx.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 1`) }()

	if isWisp {
		err = updateWispID(ctx, tx, oldID, newID, issue, actor)
	} else {
		err = updateIssueID(ctx, tx, oldID, newID, issue, actor)
	}
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 1`)
	if err != nil {
		return fmt.Errorf("failed to re-enable foreign key checks: %w", err)
	}

	return tx.Commit()
}

// updateIssueID renames a regular issue in the issues table and its auxiliary tables.
func updateIssueID(ctx context.Context, tx *sql.Tx, oldID, newID string, issue *types.Issue, actor string) error {
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

	// Update references in auxiliary tables
	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE events SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update events: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE labels SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE comments SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update comments: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE issue_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE compaction_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update compaction_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE child_counters SET parent_id = ? WHERE parent_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update child_counters: %w", err)
	}

	// Update references in wisp tables
	_, err = tx.ExecContext(ctx, `UPDATE wisp_dependencies SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in wisp_dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in wisp_dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_events SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update wisp_events: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_labels SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update wisp_labels: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_comments SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update wisp_comments: %w", err)
	}

	// Record rename event
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	if err != nil {
		return fmt.Errorf("failed to record rename event: %w", err)
	}

	return nil
}

// updateWispID renames a wisp in the wisps table and its wisp_* auxiliary tables.
func updateWispID(ctx context.Context, tx *sql.Tx, oldID, newID string, issue *types.Issue, actor string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE wisps
		SET id = ?, title = ?, description = ?, design = ?, acceptance_criteria = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`, newID, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes, time.Now().UTC(), oldID)
	if err != nil {
		return fmt.Errorf("failed to update wisp ID: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("wisp not found: %s", oldID)
	}

	// Update references in wisp auxiliary tables
	_, err = tx.ExecContext(ctx, `UPDATE wisp_dependencies SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in wisp_dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in wisp_dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_events SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update wisp_events: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_labels SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update wisp_labels: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE wisp_comments SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update wisp_comments: %w", err)
	}

	// Record rename event in wisp_events
	_, err = tx.ExecContext(ctx, `
		INSERT INTO wisp_events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	if err != nil {
		return fmt.Errorf("failed to record wisp rename event: %w", err)
	}

	return nil
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
