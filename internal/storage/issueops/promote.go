package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/steveyegge/beads/internal/storage"
)

// PromoteFromEphemeralInTx promotes a wisp to a permanent issue within a transaction.
// Copies the issue, labels, dependencies, events, and comments from wisp tables
// to permanent tables, then deletes from wisp tables.
//
//nolint:gosec // G201: table names are hardcoded constants
func PromoteFromEphemeralInTx(ctx context.Context, tx *sql.Tx, id string, actor string) error {
	// Verify the ID is an active wisp.
	if !IsActiveWispInTx(ctx, tx, id) {
		return fmt.Errorf("wisp %s not found", id)
	}

	// Get the wisp issue data.
	issue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("get wisp for promote: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("wisp %s not found", id)
	}

	// Clear ephemeral flag for persistent storage.
	issue.Ephemeral = false

	// Create in issues table via CreateIssueInTx.
	bc, err := NewBatchContext(ctx, tx, storage.BatchCreateOptions{
		SkipPrefixValidation: true,
	})
	if err != nil {
		return fmt.Errorf("new batch context: %w", err)
	}
	if err := CreateIssueInTx(ctx, tx, bc, issue, actor); err != nil {
		return fmt.Errorf("promote wisp to issues: %w", err)
	}

	// Copy labels: wisp_labels → labels.
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO labels (issue_id, label)
		SELECT issue_id, label FROM wisp_labels WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy labels: %v", id, err)
	}

	// Copy dependencies: wisp_dependencies → dependencies (best-effort).
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy dependencies: %v", id, err)
	}

	// Copy events: wisp_events → events (best-effort).
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
		SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy events: %v", id, err)
	}

	// Copy comments: wisp_comments → comments (best-effort).
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO comments (issue_id, author, text, created_at)
		SELECT issue_id, author, text, created_at
		FROM wisp_comments WHERE issue_id = ?
	`, id); err != nil {
		log.Printf("promote %s: failed to copy comments: %v", id, err)
	}

	// Delete from wisp tables.
	return DeleteIssueInTx(ctx, tx, id)
}
