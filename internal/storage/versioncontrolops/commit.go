package versioncontrolops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// DirtyTableTracker records which tables were modified during a transaction.
// Both DoltStore and EmbeddedDoltStore embed this in their transaction types
// to enable selective staging (DOLT_ADD per table) instead of staging everything.
type DirtyTableTracker struct {
	tables map[string]bool
}

// MarkDirty records that a tracked table was modified.
// Dolt-ignored tables (wisps, wisp_*) are skipped since they cannot be staged.
func (t *DirtyTableTracker) MarkDirty(table string) {
	if table == "wisps" || strings.HasPrefix(table, "wisp_") {
		return
	}
	if t.tables == nil {
		t.tables = make(map[string]bool)
	}
	t.tables[table] = true
}

// DirtyTables returns the set of tables that were modified.
func (t *DirtyTableTracker) DirtyTables() map[string]bool {
	return t.tables
}

// StageAndCommit stages only the specified dirty tables and creates a Dolt
// version commit. conn must be a non-transactional database connection (the
// SQL transaction should already be committed before calling this).
//
// If commitMsg is empty, no commit is created. "Nothing to commit" errors
// are treated as benign (e.g., all writes were to dolt-ignored tables).
func StageAndCommit(ctx context.Context, conn DBConn, dirtyTables map[string]bool, commitMsg, author string) error {
	if commitMsg == "" || len(dirtyTables) == 0 {
		return nil
	}

	for table := range dirtyTables {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD(?)", table); err != nil {
			return fmt.Errorf("dolt add %s: %w", table, err)
		}
	}

	_, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", commitMsg, author)
	if err != nil && !issueops.IsNothingToCommitError(err) {
		return fmt.Errorf("dolt commit: %w", err)
	}

	return nil
}
