package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// UpdateIssueID updates an issue ID and all its references.
func (s *DoltStore) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.UpdateIssueIDInTx(ctx, tx, oldID, newID, issue, actor)
	})
}

// RenameDependencyPrefix updates the prefix in all dependency records
func (s *DoltStore) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.RenameDependencyPrefixInTx(ctx, tx, oldPrefix, newPrefix)
	})
}

// RenameCounterPrefix is a no-op with hash-based IDs
func (s *DoltStore) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	// Hash-based IDs don't use counters
	return nil
}
