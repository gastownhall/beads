//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	var issue *types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+issueops.IssueSelectColumns+` FROM issues WHERE id = ?`, id)
		var err error
		issue, err = issueops.ScanIssueFrom(row)
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("get issue: %w", err)
		}

		// Fetch labels in the same connection to avoid MaxOpenConns=1 deadlock.
		rows, err := tx.QueryContext(ctx, `SELECT label FROM labels WHERE issue_id = ? ORDER BY label`, id)
		if err != nil {
			return fmt.Errorf("get issue labels: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var label string
			if err := rows.Scan(&label); err != nil {
				return fmt.Errorf("get issue labels: scan: %w", err)
			}
			issue.Labels = append(issue.Labels, label)
		}
		return rows.Err()
	})
	return issue, err
}
