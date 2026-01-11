package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// isUniqueConstraintError checks if error is a UNIQUE constraint violation
// Used to detect and handle duplicate IDs in JSONL imports gracefully
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "UNIQUE constraint failed") ||
		strings.Contains(errMsg, "constraint failed: UNIQUE")
}

// insertIssue inserts a single issue into the database.
// Uses INSERT OR IGNORE for backward compatibility with imports.
// For fresh issue creation, use insertIssueStrict instead.
func insertIssue(ctx context.Context, conn *sql.Conn, issue *types.Issue) error {
	sourceRepo := issue.SourceRepo
	if sourceRepo == "" {
		sourceRepo = "." // Default to primary repo
	}

	wisp := 0
	if issue.Ephemeral {
		wisp = 1
	}
	pinned := 0
	if issue.Pinned {
		pinned = 1
	}
	isTemplate := 0
	if issue.IsTemplate {
		isTemplate = 1
	}
	crystallizes := 0
	if issue.Crystallizes {
		crystallizes = 1
	}

	_, err := conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, external_ref, source_repo, close_reason,
			deleted_at, deleted_by, delete_reason, original_type,
			sender, ephemeral, pinned, is_template, crystallizes,
			await_type, await_id, timeout_ns, waiters, mol_type,
			event_kind, actor, target, payload,
			due_at, defer_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
		issue.AcceptanceCriteria, issue.Notes, issue.Status,
		issue.Priority, issue.IssueType, issue.Assignee,
		issue.EstimatedMinutes, issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt,
		issue.ClosedAt, issue.ExternalRef, sourceRepo, issue.CloseReason,
		issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
		issue.Sender, wisp, pinned, isTemplate, crystallizes,
		issue.AwaitType, issue.AwaitID, int64(issue.Timeout), formatJSONStringArray(issue.Waiters),
		string(issue.MolType),
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.DueAt, issue.DeferUntil,
	)
	if err != nil {
		// INSERT OR IGNORE should handle duplicates, but driver may still return error
		// Explicitly ignore UNIQUE constraint errors (expected for duplicate IDs in JSONL)
		if !isUniqueConstraintError(err) {
			return fmt.Errorf("failed to insert issue: %w", err)
		}
		// Duplicate ID detected and ignored (INSERT OR IGNORE succeeded)
	}
	return nil
}

// insertIssueStrict inserts a single issue into the database, failing on duplicates.
// This is used for fresh issue creation (CreateIssue) where duplicates indicate a bug.
// For imports where duplicates are expected, use insertIssue instead.
// GH#956: Using plain INSERT prevents FK constraint errors from silent INSERT OR IGNORE failures.
func insertIssueStrict(ctx context.Context, conn *sql.Conn, issue *types.Issue) error {
	sourceRepo := issue.SourceRepo
	if sourceRepo == "" {
		sourceRepo = "." // Default to primary repo
	}

	wisp := 0
	if issue.Ephemeral {
		wisp = 1
	}
	pinned := 0
	if issue.Pinned {
		pinned = 1
	}
	isTemplate := 0
	if issue.IsTemplate {
		isTemplate = 1
	}
	crystallizes := 0
	if issue.Crystallizes {
		crystallizes = 1
	}

	_, err := conn.ExecContext(ctx, `
		INSERT INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, external_ref, source_repo, close_reason,
			deleted_at, deleted_by, delete_reason, original_type,
			sender, ephemeral, pinned, is_template, crystallizes,
			await_type, await_id, timeout_ns, waiters, mol_type,
			event_kind, actor, target, payload,
			due_at, defer_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
		issue.AcceptanceCriteria, issue.Notes, issue.Status,
		issue.Priority, issue.IssueType, issue.Assignee,
		issue.EstimatedMinutes, issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt,
		issue.ClosedAt, issue.ExternalRef, sourceRepo, issue.CloseReason,
		issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
		issue.Sender, wisp, pinned, isTemplate, crystallizes,
		issue.AwaitType, issue.AwaitID, int64(issue.Timeout), formatJSONStringArray(issue.Waiters),
		string(issue.MolType),
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.DueAt, issue.DeferUntil,
	)
	if err != nil {
		return fmt.Errorf("failed to insert issue: %w", err)
	}
	return nil
}

// insertIssues bulk inserts multiple issues using a prepared statement
func insertIssues(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	stmt, err := conn.PrepareContext(ctx, `
		INSERT OR IGNORE INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, external_ref, source_repo, close_reason,
			deleted_at, deleted_by, delete_reason, original_type,
			sender, ephemeral, pinned, is_template, crystallizes,
			await_type, await_id, timeout_ns, waiters, mol_type,
			event_kind, actor, target, payload,
			due_at, defer_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issue := range issues {
		sourceRepo := issue.SourceRepo
		if sourceRepo == "" {
			sourceRepo = "." // Default to primary repo
		}

		wisp := 0
		if issue.Ephemeral {
			wisp = 1
		}
		pinned := 0
		if issue.Pinned {
			pinned = 1
		}
		isTemplate := 0
		if issue.IsTemplate {
			isTemplate = 1
		}
		crystallizes := 0
		if issue.Crystallizes {
			crystallizes = 1
		}

		_, err = stmt.ExecContext(ctx,
			issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
			issue.AcceptanceCriteria, issue.Notes, issue.Status,
			issue.Priority, issue.IssueType, issue.Assignee,
			issue.EstimatedMinutes, issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt,
			issue.ClosedAt, issue.ExternalRef, sourceRepo, issue.CloseReason,
			issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
			issue.Sender, wisp, pinned, isTemplate, crystallizes,
			issue.AwaitType, issue.AwaitID, int64(issue.Timeout), formatJSONStringArray(issue.Waiters),
			string(issue.MolType),
			issue.EventKind, issue.Actor, issue.Target, issue.Payload,
			issue.DueAt, issue.DeferUntil,
		)
		if err != nil {
			// INSERT OR IGNORE should handle duplicates, but driver may still return error
			// Explicitly ignore UNIQUE constraint errors (expected for duplicate IDs in JSONL)
			if !isUniqueConstraintError(err) {
				return fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
			}
			// Duplicate ID detected and ignored (INSERT OR IGNORE succeeded)
		}
	}
	return nil
}
