package issueops

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// These tests pin the bda-lbjv contract: the merge count a rename reports is
// MEASURED from what the INSERT IGNORE actually affected, never predicted
// from a snapshot read taken earlier in the transaction.
//
// The race they encode: an issue carries oldLabel; a concurrent
// AddLabel(issue, newLabel) COMMITS after this transaction's snapshot is
// established but before its INSERT IGNORE executes. Under REPEATABLE READ
// the insert sees the committed row and no-ops (correct - that IS the
// merge), but a merge count computed from the earlier snapshot read reports
// 0. The data was always fine; the count was schedule-dependent. sqlmock
// scripts exactly that observable: the SELECT-visible state says "no
// carrier of newLabel", the insert's rows-affected says one row already
// existed. A count derived from rows-affected reads 1; the old
// check-then-insert shape read 0 against this same script.

// expectRenameEventScript scripts the per-issue event mint that follows the
// label sweep (InsertDerivedEvent: same-content SELECT + INSERT). The
// journal half (RecordEventInTx) no-ops because neither the context key nor
// the per-tx registration enables it here.
func expectRenameEventScript(mock sqlmock.Sqlmock, times int) {
	for i := 0; i < times; i++ {
		mock.ExpectQuery(`SELECT id FROM events`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectExec(`INSERT INTO events`).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func TestRenameLabelInPlane_ConcurrentAddLabelStillCountsMerge(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	// Snapshot read: i1 carries oldLabel. (No read of newLabel's carriers
	// exists any more - the fix removed it; nothing here scripts one, so a
	// regression reintroducing the check-then-insert SELECT fails loudly on
	// an unexpected query.)
	mock.ExpectQuery(`SELECT issue_id FROM labels WHERE label = \?`).
		WithArgs("old").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("i1"))
	// The concurrently-committed (i1, new) row makes the INSERT IGNORE a
	// no-op: zero rows affected.
	mock.ExpectExec(`INSERT IGNORE INTO labels`).
		WithArgs("i1", "new").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// The delete is ID-SCOPED (label AND issue_id IN (...)): an unrestricted
	// `WHERE label = ?` would let a current-read engine remove a row a
	// concurrent AddLabel(oldLabel) committed to an issue OUTSIDE the
	// snapshot - deleting a label the sweep never renamed, counted or
	// journaled. sqlmock's WithArgs is the pin: a regression back to the
	// unrestricted form fails on the argument mismatch.
	mock.ExpectExec(`DELETE FROM labels WHERE label = \? AND issue_id IN`).
		WithArgs("old", "i1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectRenameEventScript(mock, 1)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	renamed, merged, ids, err := renameLabelInPlane(ctx, tx, "labels", "events", "old", "new", "tester")
	if err != nil {
		t.Fatalf("renameLabelInPlane: %v", err)
	}
	if renamed != 1 {
		t.Errorf("renamed = %d, want 1", renamed)
	}
	if merged != 1 {
		t.Errorf("merged = %d, want 1 (the concurrently-committed row IS a merge; a snapshot-read count reports 0 here)", merged)
	}
	if len(ids) != 1 || ids[0] != "i1" {
		t.Errorf("ids = %v, want [i1]", ids)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRenameLabelInPlane_CleanRenameCountsZeroMerges(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT issue_id FROM labels WHERE label = \?`).
		WithArgs("old").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("i1").AddRow("i2"))
	// Both rows genuinely inserted: nothing carried newLabel.
	mock.ExpectExec(`INSERT IGNORE INTO labels`).
		WithArgs("i1", "new", "i2", "new").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`DELETE FROM labels WHERE label = \? AND issue_id IN`).
		WithArgs("old", "i1", "i2").
		WillReturnResult(sqlmock.NewResult(0, 2))
	expectRenameEventScript(mock, 2)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	renamed, merged, _, err := renameLabelInPlane(ctx, tx, "labels", "events", "old", "new", "tester")
	if err != nil {
		t.Fatalf("renameLabelInPlane: %v", err)
	}
	if renamed != 2 {
		t.Errorf("renamed = %d, want 2", renamed)
	}
	if merged != 0 {
		t.Errorf("merged = %d, want 0", merged)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
