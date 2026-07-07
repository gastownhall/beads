package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

//nolint:gosec // G201: table names are hardcoded constants
func PromoteFromEphemeralInTx(ctx context.Context, tx *sql.Tx, id string, actor string) error {
	if !IsActiveWispInTx(ctx, tx, id) {
		return fmt.Errorf("wisp %s not found", id)
	}

	issue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("get wisp for promote: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("wisp %s not found", id)
	}

	issue.Ephemeral = false

	bc, err := NewBatchContext(ctx, tx, storage.BatchCreateOptions{
		SkipPrefixValidation: true,
	})
	if err != nil {
		return fmt.Errorf("new batch context: %w", err)
	}
	if err := PrepareIssueForInsert(issue, bc.CustomStatuses, bc.CustomTypes); err != nil {
		return fmt.Errorf("promote wisp to issues: %w", err)
	}
	if _, _, err := InsertIssueIfNew(ctx, tx, "issues", issue, storage.BatchCreateOptions{}); err != nil {
		return fmt.Errorf("promote wisp to issues: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO labels (issue_id, label)
		SELECT issue_id, label FROM wisp_labels WHERE issue_id = ?
	`, id); err != nil {
		return fmt.Errorf("copy labels for promoted wisp %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wisp_labels WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete copied wisp labels for promoted wisp %s: %w", id, err)
	}

	// Carry id across promotion. Both tables derive id deterministically from the
	// same (issue_id, target) key, so the wisp edge's id is exactly the id a
	// direct dependency on that edge would get; copying it (rather than letting a
	// DEFAULT mint a fresh random one) keeps the promoted edge merge-safe and is
	// required now that dependencies.id has no DEFAULT (#4259).
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id)
		SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies WHERE issue_id = ?
	`, id); err != nil {
		return fmt.Errorf("copy dependencies for promoted wisp %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wisp_dependencies WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete copied wisp dependencies for promoted wisp %s: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events WHERE issue_id = ?
	`, id); err != nil {
		return fmt.Errorf("copy events for promoted wisp %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wisp_events WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete copied wisp events for promoted wisp %s: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO comments (id, issue_id, author, text, created_at)
		SELECT id, issue_id, author, text, created_at
		FROM wisp_comments WHERE issue_id = ?
	`, id); err != nil {
		return fmt.Errorf("copy comments for promoted wisp %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wisp_comments WHERE issue_id = ?`, id); err != nil {
		return fmt.Errorf("delete copied wisp comments for promoted wisp %s: %w", id, err)
	}

	if err := RetargetInboundDependenciesToIssueInTx(ctx, tx, id); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM wisps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete promoted wisp row %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get promoted wisp rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("wisp %s not found", id)
	}

	affectedIssues, affectedWisps, aerr := AffectedByStatusChangeInTx(ctx, tx, id)
	if aerr != nil {
		return fmt.Errorf("affected by promote for %s: %w", id, aerr)
	}
	if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return fmt.Errorf("recompute is_blocked after promote for %s: %w", id, err)
	}
	return nil
}
