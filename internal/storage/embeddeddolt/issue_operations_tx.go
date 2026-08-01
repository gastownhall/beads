//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

func (s *EmbeddedDoltStore) runIssueOperationTx(ctx context.Context, commitMsg string, fn func(*sql.Tx) (storageissueops.ChangedTables, error)) error {
	// An empty message is runTransaction's existing "write, but do not version
	// commit" signal — the same one CLI batch mode already uses for its
	// RunInTransaction writes.
	if storageissueops.VersionCommitDeferred(ctx) {
		commitMsg = ""
	}
	return s.runTransaction(ctx, commitMsg, func(tx *embeddedTransaction) error {
		tables, err := fn(tx.tx)
		if err != nil {
			return err
		}
		for table := range tables {
			tx.dirty.MarkDirty(table)
		}
		return nil
	})
}
