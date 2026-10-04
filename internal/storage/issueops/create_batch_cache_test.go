package issueops

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/types"
)

// TestFlushAuxEventsMintsThePerRowIDs pins that the buffered events flush
// assigns each row the id InsertDerivedEvent would have: the lowest ordinal of
// its digest not already held by a same-content row — counting rows already
// stored and rows minted earlier in the same flush — while a different-content
// row with the same issue and second is not counted.
func TestFlushAuxEventsMintsThePerRowIDs(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	const at = "2026-01-02 03:04:05"
	created := normalizeAuxEvent("events", AuxEvent{
		IssueID: "bd-1", EventType: types.EventCreated, Actor: "importer",
		OldValue: str(""), NewValue: str(""), CreatedAt: at,
	})
	label := normalizeAuxEvent("events", AuxEvent{
		IssueID: "bd-1", EventType: types.EventLabelAdded, Actor: "importer",
		Comment: str("Added label: x"), CreatedAt: at,
	})
	createdDigest, labelDigest := auxEventDigest(created), auxEventDigest(label)
	storedCreated := rowid.New("events", 0, createdDigest)

	mock.ExpectQuery(regexp.QuoteMeta("FROM events")).
		WithArgs("bd-1", at).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "event_type", "actor", "old_value", "new_value", "comment", "created_at"}).
			// A stored same-content created row holds ordinal 0.
			AddRow(storedCreated, "bd-1", string(types.EventCreated), "importer", "", "", nil, at).
			// Same issue and second, different content: not counted.
			AddRow("legacy-random-id", "bd-1", string(types.EventLabelAdded), "importer", nil, nil, "Added label: y", at))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO events")).
		WithArgs(
			rowid.New("events", 1, createdDigest), "bd-1", string(types.EventCreated), "importer", str(""), str(""), sql.NullString{}, at,
			rowid.New("events", 2, createdDigest), "bd-1", string(types.EventCreated), "importer", str(""), str(""), sql.NullString{}, at,
			rowid.New("events", 0, labelDigest), "bd-1", string(types.EventLabelAdded), "importer", sql.NullString{}, sql.NullString{}, str("Added label: x"), at,
		).
		WillReturnResult(sqlmock.NewResult(0, 3))

	cache := &createBatchCache{}
	cache.bufferEvent("events", created)
	cache.bufferEvent("events", created)
	cache.bufferEvent("events", label)
	if err := cache.flushEvents(ctx, tx); err != nil {
		t.Fatalf("flushEvents: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestDepBatchGraphTracksTheStoredRows pins the graph's bookkeeping across the
// dependency pass's inserts: an unseen pair is added with the inserted type
// without a read; a pair it already holds is re-read, because a matched
// duplicate (which keeps the stored type) and a real insert both report
// RowsAffected 1 on a clientFoundRows connection.
func TestDepBatchGraphTracksTheStoredRows(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	l := &depBatchLookups{graph: newDepGraph()}
	l.graph.add(depEdgeKey{table: "dependencies", source: "bd-b", target: "bd-a"}, types.DepRelated)

	// New pair: no read, and the scheduling walk sees it at once.
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "bd-c", DependsOnID: "bd-b", Type: types.DepBlocks}, 1); err != nil {
		t.Fatalf("recordInsert(new): %v", err)
	}
	if !l.graph.reaches("bd-c", "bd-b", false) {
		t.Fatal("new blocks edge not walkable")
	}
	// Known pair reported as written: re-read; the stored row kept "related".
	mock.ExpectQuery(regexp.QuoteMeta("SELECT type FROM dependencies WHERE issue_id = ? AND "+DepTargetExpr+" = ?")).
		WithArgs("bd-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"type"}).AddRow(string(types.DepRelated)))
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "bd-b", DependsOnID: "bd-a", Type: types.DepParentChild}, 1); err != nil {
		t.Fatalf("recordInsert(known): %v", err)
	}
	if l.graph.reaches("bd-b", "bd-a", true) || l.graph.reaches("bd-c", "bd-a", false) {
		t.Fatal("a matched duplicate must not add the incoming type's edge")
	}
	// Nothing written: nothing changes, nothing is read.
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "bd-a", DependsOnID: "bd-c", Type: types.DepBlocks}, 0); err != nil {
		t.Fatalf("recordInsert(noop): %v", err)
	}
	if l.graph.reaches("bd-a", "bd-c", false) {
		t.Fatal("an unwritten edge must not be walkable")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
