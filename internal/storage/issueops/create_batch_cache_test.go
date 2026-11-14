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
		l.graph.loadedOut[id] = true
		l.graph.loadedIn[id] = true
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

// TestDepGraphSearchesFromBothEnds pins the lazy, bidirectional search: each
// step expands the smaller frontier, loading only that level's unloaded nodes
// in that direction (outgoing through issue_id, incoming through the three
// target columns), never the whole edge tables and never a node twice; an
// incoming read ignores a row matched through a column that is not its
// target; and a write whose pair the graph cannot vouch for is re-read
// rather than guessed.
func TestDepGraphSearchesFromBothEnds(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()
	type row = [3]string
	expect := func(incoming bool, ids []string, rows map[string][]row) {
		args := make([]driver.Value, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		for _, table := range []string{"dependencies", "wisp_dependencies"} {
			cols := []string{"issue_id"}
			if incoming {
				cols = []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}
			}
			for i, col := range cols {
				r := sqlmock.NewRows([]string{"issue_id", "target", "type"})
				if i == 0 {
					for _, x := range rows[table] {
						r.AddRow(x[0], x[1], x[2])
					}
				}
				q := "SELECT issue_id, " + DepTargetExpr + ", type FROM " + table + " WHERE " + col + " IN ("
				mock.ExpectQuery(regexp.QuoteMeta(q)).WithArgs(args...).WillReturnRows(r)
			}
		}
	}
	g := newDepGraph()
	// Stored: a -> b (blocks), b -> c (parent-child), w -> a (wisp table,
	// blocks). Is a reachable from c? (No.) Is c reachable from w? (Yes.)
	// Step 1, frontiers {c} and {a} tie: forward from c, which has no edges.
	expect(false, []string{"c"}, nil)
	if ok, err := g.reaches(ctx, tx, "c", "a", false); err != nil || ok {
		t.Fatalf("reaches(c, a) = %v, %v; want false, nil", ok, err)
	}
	// Forward from w: w -> a; then backward from c (smaller? tie: forward
	// again from {a}): a -> b; then {b} vs {c}: forward b -> c meets c.
	expect(false, []string{"w"}, map[string][]row{"wisp_dependencies": {{"w", "a", "blocks"}}})
	expect(false, []string{"a"}, map[string][]row{"dependencies": {{"a", "b", "blocks"}}})
	expect(false, []string{"b"}, map[string][]row{"dependencies": {{"b", "c", "parent-child"}}})
	if ok, err := g.reaches(ctx, tx, "w", "c", false); err != nil || !ok {
		t.Fatalf("reaches(w, c) = %v, %v; want true, nil", ok, err)
	}
	// Backward: once the forward frontier {x, y} outgrows the backward one,
	// the search keeps reading incoming rows — c, b, a, w — and never loads
	// x or y. A row matched through a non-target column is ignored, and rows
	// already known (b -> c, a -> b, w -> a) are not counted again.
	g.loadedOut["s"] = true
	g.add(depEdgeKey{table: "dependencies", source: "s", target: "x"}, types.DepBlocks)
	g.add(depEdgeKey{table: "dependencies", source: "s", target: "y"}, types.DepBlocks)
	expect(true, []string{"c"}, map[string][]row{"dependencies": {{"b", "c", "parent-child"}, {"q", "elsewhere", "blocks"}}})
	expect(true, []string{"b"}, map[string][]row{"dependencies": {{"a", "b", "blocks"}}})
	expect(true, []string{"a"}, map[string][]row{"wisp_dependencies": {{"w", "a", "blocks"}}})
	expect(true, []string{"w"}, nil)
	if ok, err := g.reaches(ctx, tx, "s", "c", false); err != nil || ok {
		t.Fatalf("reaches(s, c) = %v, %v; want false, nil", ok, err)
	}
	if _, known := g.rows[depEdgeKey{table: "dependencies", source: "q", target: "elsewhere"}]; known {
		t.Fatal("a row matched through a non-target column was taken as an incoming edge")
	}
	if n := g.out.parents["b"]["c"]; n != 1 {
		t.Fatalf("edge b -> c counted %d times after being read from both ends, want 1", n)
	}
	// A written pair neither end vouches for is re-read, not assumed new.
	l := &depBatchLookups{graph: g}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT type FROM dependencies WHERE issue_id = ? AND "+DepTargetExpr+" = ?")).
		WithArgs("m", "n").WillReturnRows(sqlmock.NewRows([]string{"type"}).AddRow("related"))
	if err := l.recordInsert(ctx, tx, "dependencies", &types.Dependency{IssueID: "m", DependsOnID: "n", Type: types.DepBlocks}, 1); err != nil {
		t.Fatalf("recordInsert: %v", err)
	}
	if g.out.sched["m"]["n"] != 0 {
		t.Fatal("a matched duplicate's incoming type was taken as the stored one")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
