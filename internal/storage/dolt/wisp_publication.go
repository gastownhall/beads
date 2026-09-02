package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// wispDurableChanges is the publication contract for direct wisp mutations.
// Wisp rows and their auxiliary tables commit with the SQL transaction, but a
// cross-plane mutation can also change versioned issues, dependencies, or
// counters. Feeding every reported table through DirtyTableTracker keeps the
// canonical dolt-ignore rules in one place and prevents ignored state from
// being handed to DOLT_ADD.
type wispDurableChanges struct {
	dirty versioncontrolops.DirtyTableTracker
}

func (c *wispDurableChanges) Mark(table string, changed bool) {
	if changed {
		c.dirty.MarkDirty(table)
	}
}

func (c *wispDurableChanges) Merge(changedTables map[string]bool) {
	for table, changed := range changedTables {
		c.Mark(table, changed)
	}
}

func (c *wispDurableChanges) Tables() []string {
	return sortedDirtyTables(c.dirty.DirtyTables())
}

// publishWispDurableChangesInTx stages and versions only the durable rows a
// direct wisp mutation actually changed. Callers managed by withRetryTx use
// this helper and let the wrapper commit SQL afterward.
func (s *DoltStore) publishWispDurableChangesInTx(
	ctx context.Context,
	tx *sql.Tx,
	changes wispDurableChanges,
	commitMsg string,
) error {
	tables := changes.Tables()
	if len(tables) == 0 {
		return nil
	}
	return s.doltAddAndCommitInTx(ctx, tx, tables, commitMsg)
}

// commitWispMutationTx is the manual-transaction counterpart: publish any
// durable cross-plane effects first, then commit the ignored-plane SQL writes.
func (s *DoltStore) commitWispMutationTx(
	ctx context.Context,
	tx *sql.Tx,
	changes wispDurableChanges,
	commitMsg, sqlCommitOp string,
) error {
	if err := s.publishWispDurableChangesInTx(ctx, tx, changes, commitMsg); err != nil {
		return err
	}
	return s.commitSQLTx(ctx, sqlCommitOp, tx)
}
