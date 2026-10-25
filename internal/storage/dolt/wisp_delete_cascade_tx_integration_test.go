//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// TestWispDeleteCascade_TransactionDeleteIssue covers the delete path #5343
// left uncovered: Transaction.DeleteIssue (what `bd mol burn` uses) goes
// through issueops.DeleteIssueInTx -> deleteIssueRowInTx rather than
// deleteWisp/deleteWispBatchTx, and removed only the wisps row and the
// dependencies edges. On stores without the wisp aux FKs (dropped by
// setupWispCascadeStore) every burned wisp orphaned its wisp_labels,
// wisp_events, wisp_comments and wisp_child_counters rows (#6487).
func TestWispDeleteCascade_TransactionDeleteIssue(t *testing.T) {
	store := setupWispCascadeStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	wisp := createTestWisp(t, ctx, store, "tx-delete wisp")
	other := createTestWisp(t, ctx, store, "tx-delete bystander wisp")
	seedWispAuxRows(t, ctx, store.db, wisp.ID)
	seedWispAuxRows(t, ctx, store.db, other.ID)
	bystanderBefore := make([]int, len(wispAuxTables))
	for i, tc := range wispAuxTables {
		bystanderBefore[i] = countWispAuxRows(t, ctx, store.db, tc.table, tc.column, other.ID)
	}

	if err := store.RunInTransaction(ctx, "test: burn wisp", func(tx storage.Transaction) error {
		return tx.DeleteIssue(ctx, wisp.ID)
	}); err != nil {
		t.Fatalf("Transaction.DeleteIssue: %v", err)
	}

	assertWispAuxRowsGone(t, ctx, store.db, wisp.ID)

	// The delete must be scoped to the deleted wisp: a bystander's rows stay.
	for i, tc := range wispAuxTables {
		if got := countWispAuxRows(t, ctx, store.db, tc.table, tc.column, other.ID); got != bystanderBefore[i] {
			t.Errorf("bystander wisp %s: %s rows changed from %d to %d", other.ID, tc.table, bystanderBefore[i], got)
		}
	}
}

func countWispAuxRows(t *testing.T, ctx context.Context, db *sql.DB, table, column, id string) int {
	t.Helper()
	var count int
	//nolint:gosec // G201: table/column come from the fixed wispAuxTables literal, not input.
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", table, column)
	if err := db.QueryRowContext(ctx, q, id).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
