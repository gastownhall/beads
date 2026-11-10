//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"slices"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
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
// full commit as a *storage.UncommittedChangesError, except bd's own
// clone-local tables (schema.OwnedIgnorePolicy), whose changes a commit is
// meant to leave behind. Only operator-visible status rows count: a new table
// some pattern ignores is hidden from dolt_status by Dolt itself, so it is
// outside what this check can see.
func verifyCommitComplete(ctx context.Context, tx *sql.Tx, committed bool) error {
	tables, err := readStatusTables(ctx, tx)
	if err != nil {
		return verificationFailed(committed, "read dolt_status", err)
	}
	if len(tables) == 0 {
		return nil
	}
	// The policy is read only when something is dirty, so the healthy path
	// costs one status query.
	policy, err := schema.LoadOwnedIgnorePolicy(ctx, tx)
	if err != nil {
		return verificationFailed(committed, "read bd-owned ignore policy", err)
	}
	remaining := slices.DeleteFunc(tables, policy.OwnsTable)
	if len(remaining) == 0 {
		return nil
	}
	return &storage.UncommittedChangesError{
		Tables:         remaining,
		IgnorePatterns: enabledNoncanonicalIgnorePatterns(ctx, tx, policy),
		Committed:      committed,
	}
}

// verificationFailed reports a required read after DOLT_COMMIT that failed:
// the store cannot say whether the working set is clean, so it neither
// reports the commit as complete nor tolerates it as a no-op. The payload
// keeps what DOLT_COMMIT itself answered.
func verificationFailed(committed bool, operation string, cause error) error {
	outcome := storage.CommitOutcomeNothingToCommit
	if committed {
		outcome = storage.CommitOutcomeSucceeded
	}
	return &storage.CommitVerificationError{Outcome: outcome, Operation: operation, Cause: cause}
}

func readStatusTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(tables)
	return slices.Compact(tables), nil
}

// enabledNoncanonicalIgnorePatterns is the optional diagnostic: the enabled
// dolt_ignore patterns outside bd's canonical clone-local set, which may
// explain why the commit skipped the remaining tables but do not prove it or
// who added them. A failed read returns nil so the residual-change finding
// still surfaces.
func enabledNoncanonicalIgnorePatterns(ctx context.Context, tx *sql.Tx, policy schema.OwnedIgnorePolicy) []string {
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
		if !policy.OwnsPattern(pattern) {
			patterns = append(patterns, pattern)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return patterns
}
