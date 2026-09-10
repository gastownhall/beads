//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

func (s *EmbeddedDoltStore) runIssueOperationTx(ctx context.Context, commitMsg string, fn func(*sql.Tx) (storageissueops.ChangedTables, error)) error {
	return s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		tables, err := fn(tx)
		return tables, commitMsg, err
	})
}

// runIssueOperationTxWithMessage is runIssueOperationTx for an operation whose
// commit message is only known once the body has run — a ready claim names the
// id it won. Deferral still wins over whatever the body composes: an empty
// message is runTransaction's existing "write, but do not version commit"
// signal, the same one CLI batch mode already uses for its RunInTransaction
// writes.
func (s *EmbeddedDoltStore) runIssueOperationTxWithMessage(ctx context.Context, fn func(*sql.Tx) (storageissueops.ChangedTables, string, error)) error {
	deferred := storageissueops.VersionCommitDeferred(ctx)
	return s.runTransactionWithMessage(ctx, func(tx *embeddedTransaction) (string, error) {
		tables, commitMsg, err := fn(tx.tx)
		if err != nil {
			return "", err
		}
		for table := range tables {
			tx.dirty.MarkDirty(table)
		}
		if deferred {
			return "", nil
		}
		return commitMsg, nil
	})
}
