package dolt

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestDemoteToWispCoordinatesBeforeItTouchesTheRow pins that the Dolt store's
// own persistence move (DemoteToWisp, reached from the storage-level
// UpdateIssue and UpdateIssueChecked with wisp or no_history) takes the id's
// create-coordination cell before its first read of the row, as the shared
// persistence move and promotion do. A retaining sweep takes the same cell
// before it deletes and tombstones the row, so the demotion either runs first
// or finds the row gone: it never re-inserts into wisps a row the sweep
// retired.
func TestDemoteToWispCoordinatesBeforeItTouchesTheRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(context.Canceled)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	store := &DoltStore{}
	if err := store.demoteToWispInTx(context.Background(), tx, "bd-1", map[string]interface{}{"wisp": true}, "actor"); err == nil {
		t.Fatal("demoteToWispInTx() = nil; the mock fails the coordination write")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
