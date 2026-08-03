package dolt

import (
	"context"
	"database/sql"
	"sort"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

func (s *DoltStore) runIssueOperationTx(ctx context.Context, commitMsg string, fn func(*sql.Tx) (storageissueops.ChangedTables, error)) error {
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		tables, err := fn(tx)
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
