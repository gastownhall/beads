package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
	for _, id := range []string{"bd-a", "bd-b", "bd-c"} {
		l.graph.loaded[id] = true
	}
	l.graph.add(depEdgeKey{table: "dependencies", source: "bd-b", target: "bd-a"}, types.DepRelated)
	reaches := func(from, to string, parentsOnly bool) bool {
		t.Helper()
		ok, err := l.graph.reaches(ctx, tx, from, to, parentsOnly)
		if err != nil {
			t.Fatalf("reaches: %v", err)
		}
		return ok
	}

	// New pair: no read, and the scheduling walk sees it at once.
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "bd-c", DependsOnID: "bd-b", Type: types.DepBlocks}, 1); err != nil {
		t.Fatalf("recordInsert(new): %v", err)
	}
	if !reaches("bd-c", "bd-b", false) {
		t.Fatal("new blocks edge not walkable")
	}
	// Known pair reported as written: re-read; the stored row kept "related".
	mock.ExpectQuery(regexp.QuoteMeta("SELECT type FROM dependencies WHERE issue_id = ? AND "+DepTargetExpr+" = ?")).
		WithArgs("bd-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"type"}).AddRow(string(types.DepRelated)))
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "bd-b", DependsOnID: "bd-a", Type: types.DepParentChild}, 1); err != nil {
		t.Fatalf("recordInsert(known): %v", err)
	}
	if reaches("bd-b", "bd-a", true) || reaches("bd-c", "bd-a", false) {
		t.Fatal("a matched duplicate must not add the incoming type's edge")
	}
	// Nothing written: nothing changes, nothing is read.
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "bd-a", DependsOnID: "bd-c", Type: types.DepBlocks}, 0); err != nil {
		t.Fatalf("recordInsert(noop): %v", err)
	}
	if reaches("bd-a", "bd-c", false) {
		t.Fatal("an unwritten edge must not be walkable")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestIssueInsertChunksHonorsRowsAndBytes pins the multi-row INSERT budget:
// at most issueInsertRowsPerStatement rows, a new statement before the
// estimated bytes would pass issueInsertBytesPerStatement, and an oversized
// row still travels (alone) rather than being dropped.
func TestIssueInsertChunksHonorsRowsAndBytes(t *testing.T) {
	small := func(n int) []*types.Issue {
		out := make([]*types.Issue, n)
		for i := range out {
			out[i] = &types.Issue{ID: fmt.Sprintf("bd-%d", i), Title: "t"}
		}
		return out
	}
	sizes := func(chunks [][]*types.Issue) []int {
		var out []int
		for _, c := range chunks {
			out = append(out, len(c))
		}
		return out
	}
	if got := sizes(issueInsertChunks(small(250))); fmt.Sprint(got) != "[100 100 50]" {
		t.Fatalf("250 small rows chunked as %v, want [100 100 50]", got)
	}
	big := strings.Repeat("x", 10<<20)
	huge := strings.Repeat("y", 20<<20)
	issues := []*types.Issue{
		{ID: "bd-a", Description: big},
		{ID: "bd-b", Notes: big},
		{ID: "bd-c", Title: "small"},
		{ID: "bd-d", Description: huge},
		{ID: "bd-e", Title: "small"},
	}
	if got := sizes(issueInsertChunks(issues)); fmt.Sprint(got) != "[1 2 1 1]" {
		t.Fatalf("long-text rows chunked as %v, want [1 2 1 1]", got)
	}
}

// TestInsertIssueRowsReplaysAFailedStatement pins the multi-row INSERT's
// failure handling: the rows of a refused statement are written one at a
// time, the batch continues when every row lands, and the first row that
// cannot land fails it with the per-row path's error.
func TestInsertIssueRowsReplaysAFailedStatement(t *testing.T) {
	issues := []*types.Issue{{ID: "bd-1", Title: "one"}, {ID: "bd-2", Title: "two"}}
	multi := regexp.QuoteMeta("INSERT INTO issues")
	t.Run("replay succeeds", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectExec(multi).WillReturnError(errors.New("packet too large"))
		mock.ExpectExec(multi).WithArgs(issueRowArgMatchers("bd-1")...).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(multi).WithArgs(issueRowArgMatchers("bd-2")...).WillReturnResult(sqlmock.NewResult(0, 1))
		if err := insertIssueRowsIntoTable(context.Background(), tx, "issues", issues, false); err != nil {
			t.Fatalf("insertIssueRowsIntoTable = %v, want nil after a clean replay", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("replay fails", func(t *testing.T) {
		db, mock, tx := beginMockTx(t)
		defer db.Close()
		mock.ExpectExec(multi).WillReturnError(errors.New("packet too large"))
		mock.ExpectExec(multi).WithArgs(issueRowArgMatchers("bd-1")...).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(multi).WithArgs(issueRowArgMatchers("bd-2")...).WillReturnError(errors.New("data too long"))
		err := insertIssueRowsIntoTable(context.Background(), tx, "issues", issues, false)
		if err == nil || !strings.Contains(err.Error(), "failed to insert issue bd-2") || !strings.Contains(err.Error(), "data too long") {
			t.Fatalf("insertIssueRowsIntoTable = %v, want the per-row error for bd-2", err)
		}
	})
}

// issueRowArgMatchers matches one single-row issue INSERT whose id is id.
func issueRowArgMatchers(id string) []driver.Value {
	args := make([]driver.Value, len(issueInsertArgs(&types.Issue{})))
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	args[0] = id
	return args
}

// TestDepGraphLoadsOnlyTheNodesItWalks pins the lazy load: a walk reads each
// BFS level's unloaded nodes in one IN-list read per table, through the
// issue_id index, never the whole edge tables, and never reads a node twice;
// a write for a source not yet loaded loads it (row included) instead of
// adding the edge a second time.
func TestDepGraphLoadsOnlyTheNodesItWalks(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	cols := []string{"issue_id", "target", "type"}
	expectLevel := func(ids []string, rows map[string][][3]string) {
		for _, table := range []string{"dependencies", "wisp_dependencies"} {
			args := make([]driver.Value, len(ids))
			for i, id := range ids {
				args[i] = id
			}
			r := sqlmock.NewRows(cols)
			for _, row := range rows[table] {
				r.AddRow(row[0], row[1], row[2])
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM " + table + " WHERE issue_id IN (")).
				WithArgs(args...).WillReturnRows(r)
		}
	}
	g := newDepGraph()
	// a -> b (blocks), a -> w (wisp table, blocks), b -> c (parent-child).
	expectLevel([]string{"a"}, map[string][][3]string{
		"dependencies":      {{"a", "b", "blocks"}},
		"wisp_dependencies": {{"a", "w", "blocks"}},
	})
	expectLevel([]string{"b", "w"}, map[string][][3]string{"dependencies": {{"b", "c", "parent-child"}}})
	expectLevel([]string{"c"}, nil)
	ok, err := g.reaches(ctx, tx, "a", "zz", false)
	if err != nil || ok {
		t.Fatalf("reaches(a, zz) = %v, %v; want false, nil", ok, err)
	}
	// Everything reached is loaded: no further reads.
	if ok, err := g.reaches(ctx, tx, "a", "c", false); err != nil || !ok {
		t.Fatalf("reaches(a, c) = %v, %v; want true, nil", ok, err)
	}
	// A written edge whose source was never loaded: load it, row and all.
	l := &depBatchLookups{graph: g}
	expectLevel([]string{"d"}, map[string][][3]string{"dependencies": {{"d", "a", "blocks"}}})
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "d", DependsOnID: "a", Type: types.DepBlocks}, 1); err != nil {
		t.Fatalf("recordInsert: %v", err)
	}
	if n := g.sched["d"]["a"]; n != 1 {
		t.Fatalf("edge d -> a counted %d times, want 1", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
