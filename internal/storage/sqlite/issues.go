package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// insertIssue inserts a single issue into the database
func insertIssue(ctx context.Context, conn *sql.Conn, issue *types.Issue) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
		issue.AcceptanceCriteria, issue.Notes, issue.Status,
		issue.Priority, issue.IssueType, issue.Assignee,
		issue.EstimatedMinutes, issue.CreatedAt, issue.UpdatedAt,
		issue.ClosedAt, issue.ExternalRef,
	)
	if err != nil {
		return fmt.Errorf("failed to insert issue: %w", err)
	}
	return nil
}

// insertIssues bulk inserts multiple issues using a prepared statement
func insertIssues(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	stmt, err := conn.PrepareContext(ctx, `
		INSERT INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issue := range issues {
		_, err = stmt.ExecContext(ctx,
			issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
			issue.AcceptanceCriteria, issue.Notes, issue.Status,
			issue.Priority, issue.IssueType, issue.Assignee,
			issue.EstimatedMinutes, issue.CreatedAt, issue.UpdatedAt,
			issue.ClosedAt, issue.ExternalRef,
		)
		if err != nil {
			return fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
		}
	}
	return nil
}
