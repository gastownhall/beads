package sqlkit

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddLabel adds a label to an issue. Passing empty table names lets issueops
// auto-route between the permanent and wisp label/event tables. Labels cannot
// change is_blocked, so this uses withWriteTx.
func (s *Store) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.AddLabelInTx(ctx, tx, "", "", issueID, label, actor)
	})
}

// RemoveLabel removes a label from an issue. issueops handles wisp routing.
func (s *Store) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.RemoveLabelInTx(ctx, tx, "", "", issueID, label, actor)
	})
}

// GetLabels retrieves all labels for an issue. The empty table argument routes
// to the correct label table automatically.
func (s *Store) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	var labels []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		labels, err = issueops.GetLabelsInTx(ctx, tx, "", issueID)
		return err
	})
	return labels, err
}

// GetIssuesByLabel retrieves all issues carrying a specific label. Both the
// id lookup and the issue hydration run in one read tx (a single *sql.Tx serves
// every table since wisps live in the same DB).
func (s *Store) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		ids, err := issueops.GetIssuesByLabelInTx(ctx, tx, label)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result, err = issueops.GetIssuesByIDsInTx(ctx, tx, ids, nil)
		return err
	})
	return result, err
}
