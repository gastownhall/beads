//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/storage"
)

// commitAllThrough runs commitAllInTx in the transaction withTx provides and,
// for a tolerant commit, checks afterwards that the working set is really
// clean (GH#5433). DOLT_COMMIT('-Am') stages only what Dolt's ignore filter
// lets through, so a tracked table covered by a dolt_ignore pattern stays
// dirty while Dolt answers "nothing to commit" (or commits everything else);
// without this check the store reported that as clean and every push sent
// nothing.
//
// The check runs inside the same transaction, but its finding is returned
// only after withTx has published it: returning the finding from the
// callback would roll the transaction back, undoing a partial commit Dolt
// already made. A publication or cleanup error from withTx wins over the
// finding, so an unconfirmed commit is never reported as a confirmed partial
// one. CommitMergeResolution's non-tolerant call is left as it was.
func commitAllThrough(ctx context.Context, withTx func(func(*sql.Tx) error) error, message string, tolerateEmpty bool) (bool, error) {
	var committed bool
	var finding error
	err := withTx(func(tx *sql.Tx) error {
		var commitErr error
		committed, commitErr = commitAllInTx(ctx, tx, message, tolerateEmpty)
		if commitErr != nil || !tolerateEmpty {
			return commitErr
		}
		finding = verifyCommitComplete(ctx, tx, committed)
		return nil
	})
	if err != nil {
		return committed, err
	}
	return committed, finding
}

// verifyCommitComplete reports the tables dolt_status still lists after a
// full commit as a *storage.UncommittedChangesError.
func verifyCommitComplete(ctx context.Context, tx *sql.Tx, committed bool) error {
	remaining, err := readStatusTables(ctx, tx)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return nil
	}
	return &storage.UncommittedChangesError{
		Tables:         remaining,
		IgnorePatterns: enabledIgnorePatterns(ctx, tx),
		Committed:      committed,
	}
}

func readStatusTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		return nil, fmt.Errorf("read dolt_status: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("read dolt_status: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read dolt_status: %w", err)
	}
	slices.Sort(tables)
	return slices.Compact(tables), nil
}

// enabledIgnorePatterns is the optional diagnostic: the enabled dolt_ignore
// patterns, a likely reason the commit skipped the remaining tables. A
// failed read returns nil so the residual-change finding still surfaces.
func enabledIgnorePatterns(ctx context.Context, tx *sql.Tx) []string {
	rows, err := tx.QueryContext(ctx, "SELECT pattern FROM dolt_ignore WHERE ignored = 1 ORDER BY pattern")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var patterns []string
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			return nil
		}
		patterns = append(patterns, pattern)
	}
	if rows.Err() != nil {
		return nil
	}
	return patterns
}
