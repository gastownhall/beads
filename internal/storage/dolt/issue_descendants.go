package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/types"
)

// GetDescendants gathers the filtered subtree in one read transaction.
func (s *DoltStore) GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = db.NewIssueSQLRepository(tx).GetDescendants(ctx, rootID, filter)
		return err
	})
	return result, err
}
