//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	var labels []string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT label FROM labels WHERE issue_id = ? ORDER BY label`, issueID)
		if err != nil {
			return fmt.Errorf("get labels: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var label string
			if err := rows.Scan(&label); err != nil {
				return fmt.Errorf("get labels: scan: %w", err)
			}
			labels = append(labels, label)
		}
		return rows.Err()
	})
	return labels, err
}

func (s *EmbeddedDoltStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)`, issueID, label); err != nil {
			return fmt.Errorf("add label: %w", err)
		}
		comment := "Added label: " + label
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (issue_id, event_type, actor, comment) VALUES (?, ?, ?, ?)`,
			issueID, types.EventLabelAdded, actor, comment); err != nil {
			return fmt.Errorf("add label: record event: %w", err)
		}
		return nil
	})
}
