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
//
// An empty author omits --author entirely, leaving attribution to the connected
// session. That is not a fallback for a missing identity but the proxied-server
// plane's normal discipline: its every other commit (uow.Tx.Commit) is
// unauthored, and passing an empty --author would be a malformed commit rather
// than an unattributed one.
func StageAndCommit(ctx context.Context, conn DBConn, dirtyTables map[string]bool, commitMsg, author string) error {
	if commitMsg == "" || len(dirtyTables) == 0 {
		return nil
	}

	// dirtyTables tracks tables touched by a write statement, but a statement
	// can succeed without changing any rows (e.g. an idempotent
	// "INSERT ... ON DUPLICATE KEY UPDATE value = VALUES(value)" re-writing the
	// same value, an INSERT IGNORE that hit a duplicate, or an UPDATE whose WHERE
	// matched nothing). Staging + committing in that case is a no-op that Dolt
	// rejects with a "nothing to commit" warning logged server-side on every call
	// — at high-frequency callers (config/metadata heartbeats, reconcile counters,
	// idempotent label/dependency writes) this floods the Dolt log.
	//
	// Cheap fast-path: if NOTHING is pending in the whole working set (excluding
	// dolt-ignored tables, which cannot be staged), skip without touching Dolt's
	// staging machinery. Note: callers like Update/Close also write an events row,
	// so a zero-rows main-table write can still be a real change — dolt_status
	// captures that correctly where a rows-affected check would not.
	pending, err := issueops.HasPendingChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("check pending changes before commit: %w", err)
	}
	if !pending {
		return nil
	}

	for table := range dirtyTables {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD(?)", table); err != nil {
			return fmt.Errorf("dolt add %s: %w", table, err)
		}
	}

	// Precise guard: HasPendingChanges above is global, but we only DOLT_ADD the
	// dirty-tracked tables. When those specific tables turn out clean (idempotent
	// no-op) while some UNRELATED table is concurrently dirty, the fast-path does
	// not fire yet staging stages nothing — so DOLT_COMMIT('-m') would still emit
	// the "nothing to commit" warning. Check the STAGED set (exactly what '-m'
	// will commit) and skip the empty commit.
	staged, err := issueops.HasStagedChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("check staged changes before commit: %w", err)
	}
	if !staged {
		return nil
	}
	if author == "" {
		_, err = conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", commitMsg)
	} else {
		_, err = conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", commitMsg, author)
	}
	if err != nil && !issueops.IsNothingToCommitError(err) {
		return fmt.Errorf("dolt commit: %w", err)
	}

	return nil
}
