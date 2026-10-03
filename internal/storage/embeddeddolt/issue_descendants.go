//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/types"
)

// GetDescendants gathers the filtered subtree in one read transaction.
func (s *EmbeddedDoltStore) GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = db.NewIssueSQLRepository(tx).GetDescendants(ctx, rootID, filter)
		return err
	})
	return result, err
}
