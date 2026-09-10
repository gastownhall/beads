package dolt

import (
	"context"
	"database/sql"
	"sort"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

func (s *DoltStore) runIssueOperationTx(ctx context.Context, commitMsg string, fn func(*sql.Tx) (storageissueops.ChangedTables, error)) error {
	return s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		tables, err := fn(tx)
		return tables, commitMsg, err
	})
}

// runIssueOperationTxWithMessage is runIssueOperationTx for an operation whose
// commit message is only known once the body has run. A ready claim names the
// id it won, and nothing outside the transaction can predict which one that
// is, so the message is composed where the selection happens.
func (s *DoltStore) runIssueOperationTxWithMessage(ctx context.Context, fn func(*sql.Tx) (storageissueops.ChangedTables, string, error)) error {
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		tables, commitMsg, err := fn(tx)
		if err != nil {
			return err
		}
		if len(tables) == 0 {
			return nil
		}
		staged := make([]string, 0, len(tables))
		for table := range tables {
			staged = append(staged, table)
		}
		sort.Strings(staged)
		return s.doltAddAndCommitInTx(ctx, tx, staged, commitMsg)
	})
}
