package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// HistoryInTx returns the complete version history for an issue by querying
// the dolt_history_issues system table. The result is ordered newest-first.
//
// The subquery wrapper avoids Dolt's max1Row optimization on PK lookup:
// dolt_history_* tables return multiple rows per PK (one per commit), but
// the query planner incorrectly assumes WHERE id=? returns one row.
func HistoryInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*storage.HistoryEntry, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			id, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, owner, created_by,
			estimated_minutes, created_at, updated_at, closed_at, close_reason,
			pinned, mol_type,
			commit_hash, committer, commit_date
		FROM (
			SELECT * FROM dolt_history_issues
		) h
		WHERE h.id = ?
		ORDER BY h.commit_date DESC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue history: %w", err)
	}
	defer rows.Close()

	var entries []*storage.HistoryEntry
	for rows.Next() {
		var issue types.Issue
		var createdAtStr, updatedAtStr sql.NullString
		var closedAt sql.NullTime
		var assignee, owner, createdBy, closeReason, molType sql.NullString
		var estimatedMinutes sql.NullInt64
		var pinned sql.NullInt64
		var commitHash, committer string
		var commitDate time.Time

		if err := rows.Scan(
			&issue.ID, &issue.Title, &issue.Description, &issue.Design, &issue.AcceptanceCriteria, &issue.Notes,
			&issue.Status, &issue.Priority, &issue.IssueType, &assignee, &owner, &createdBy,
			&estimatedMinutes, &createdAtStr, &updatedAtStr, &closedAt, &closeReason,
			&pinned, &molType,
			&commitHash, &committer, &commitDate,
		); err != nil {
			return nil, fmt.Errorf("failed to scan history: %w", err)
		}

		if createdAtStr.Valid {
			issue.CreatedAt = ParseTimeString(createdAtStr.String)
		}
		if updatedAtStr.Valid {
			issue.UpdatedAt = ParseTimeString(updatedAtStr.String)
		}
		if closedAt.Valid {
			issue.ClosedAt = &closedAt.Time
		}
		if assignee.Valid {
			issue.Assignee = assignee.String
		}
		if owner.Valid {
			issue.Owner = owner.String
		}
		if createdBy.Valid {
			issue.CreatedBy = createdBy.String
		}
		if estimatedMinutes.Valid {
			mins := int(estimatedMinutes.Int64)
			issue.EstimatedMinutes = &mins
		}
		if closeReason.Valid {
			issue.CloseReason = closeReason.String
		}
		if pinned.Valid && pinned.Int64 != 0 {
			issue.Pinned = true
		}
		if molType.Valid {
			issue.MolType = types.MolType(molType.String)
		}

		entries = append(entries, &storage.HistoryEntry{
			CommitHash: commitHash,
			Committer:  committer,
			CommitDate: commitDate,
			Issue:      &issue,
		})
	}

	return entries, rows.Err()
}
