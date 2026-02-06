package sqlite

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/types"
)

// GetEpicsEligibleForClosure returns all epics with their completion status
func (s *SQLiteStorage) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	query := `
		WITH epic_children AS (
			SELECT 
				d.depends_on_id AS epic_id,
				i.id AS child_id,
				i.status AS child_status
			FROM dependencies d
			JOIN issues i ON i.id = d.issue_id
			WHERE d.type = 'parent-child'
		),
		epic_stats AS (
			SELECT 
				epic_id,
				COUNT(*) AS total_children,
				SUM(CASE WHEN child_status = 'closed' THEN 1 ELSE 0 END) AS closed_children
			FROM epic_children
			GROUP BY epic_id
		)
		SELECT 
			i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
			i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
			i.created_at, i.updated_at, i.closed_at, i.external_ref,
			COALESCE(es.total_children, 0) AS total_children,
			COALESCE(es.closed_children, 0) AS closed_children
		FROM issues i
		LEFT JOIN epic_stats es ON es.epic_id = i.id
		WHERE i.issue_type = 'epic'
		  AND i.status != 'closed'
		ORDER BY i.priority ASC, i.created_at ASC
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, wrapDBError("query epics eligible for closure", err)
	}
	defer func() { _ = rows.Close() }()

	var results []*types.EpicStatus
	for rows.Next() {
		var epic types.Issue
		var totalChildren, closedChildren int
		var assignee sql.NullString

		err := rows.Scan(
			&epic.ID, &epic.Title, &epic.Description, &epic.Design,
			&epic.AcceptanceCriteria, &epic.Notes, &epic.Status,
			&epic.Priority, &epic.IssueType, &assignee,
			&epic.EstimatedMinutes, &epic.CreatedAt, &epic.UpdatedAt,
			&epic.ClosedAt, &epic.ExternalRef,
			&totalChildren, &closedChildren,
		)
		if err != nil {
			return nil, wrapDBError("scan epic status", err)
		}

		// Convert sql.NullString to string
		if assignee.Valid {
			epic.Assignee = assignee.String
		}

		eligibleForClose := false
		if totalChildren > 0 && closedChildren == totalChildren {
			eligibleForClose = true
		}

		results = append(results, &types.EpicStatus{
			Epic:             &epic,
			TotalChildren:    totalChildren,
			ClosedChildren:   closedChildren,
			EligibleForClose: eligibleForClose,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, wrapDBError("iterate epics", err)
	}
	return results, nil
}
