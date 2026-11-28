package issueops

import (
	"context"
	"database/sql/driver"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// The store-backed counts search answers a bounded page ids-first (#7635): a
// label-driven id scan with the page's ORDER BY and LIMIT, then the by-IDs
// counts form over exactly those ids in sqlbuild.QueryBatchSize batches. The
// predicate form it replaced groups each side table whole before the LIMIT,
// so a 50-row page cost what the full match set did. These tests pin which
// statements the search issues; the real-engine equivalence tests
// (internal/storage/embeddeddolt, internal/storage/dolt) pin that the rows
// are the predicate form's.

// idScanSQL matches the per-table id scan for a one-label filter sorted by
// created: the plain seam's id projection (searchTableInTxT), label as a JOIN.
func idScanSQL(main, labels string, limit int) string {
	return fmt.Sprintf(`^SELECT DISTINCT %[1]s\.id FROM %[1]s JOIN %[2]s label_filter_0 ON label_filter_0\.issue_id = %[1]s\.id\s+WHERE label_filter_0\.label = \?\s+ORDER BY created_at DESC, id ASC\s+LIMIT %[3]d$`,
		main, labels, limit)
}

// byIDsSQL matches one by-IDs counts statement over n ids for a table family.
func byIDsSQL(main string, n int) string {
	ph := strings.TrimSuffix(strings.Repeat(`\?,`, n), ",")
	return fmt.Sprintf(`(?s)^\s*SELECT .*FROM %s i\s.*WHERE i\.id IN \(%s\)\s*$`, regexp.QuoteMeta(main), ph)
}

// byIDsArgs is the arg list the by-IDs form binds for ids: one copy per
// injection point (see sqlbuild.SearchCountsSQL).
func byIDsArgs(tables FilterTables, ids []string, wispDeps bool) []driver.Value {
	_, args := sqlbuild.SearchCountsSQL(tables, ids, "", "", "", wispDeps, sqlbuild.CountsHydration{})
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}

func idRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

func probeRow() *sqlmock.Rows { return sqlmock.NewRows([]string{"1"}).AddRow(1) }

func pageIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%04d", prefix, i)
	}
	return ids
}

// TestSearchIssuesWithCountsBoundedPageGoesIDsFirst pins the statement
// sequence of a bounded, label-filtered counts search over both planes: per
// table, the id scan and then by-IDs hydration of exactly the ids it found —
// and never the predicate form, which sqlmock would report as an unexpected
// query.
func TestSearchIssuesWithCountsBoundedPageGoesIDsFirst(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	issueIDs := []string{"bd-2", "bd-1"}
	wispIDs := []string{"bw-9"}

	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).WillReturnRows(probeRow())
	mock.ExpectQuery(idScanSQL("issues", "labels", 3)).WithArgs("hot").WillReturnRows(idRows(issueIDs...))
	mock.ExpectQuery(byIDsSQL("issues", 2)).WithArgs(byIDsArgs(IssuesFilterTables, issueIDs, true)...).WillReturnRows(emptyCountsRows())
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnRows(probeRow())
	mock.ExpectQuery(idScanSQL("wisps", "wisp_labels", 3)).WithArgs("hot").WillReturnRows(idRows(wispIDs...))
	mock.ExpectQuery(byIDsSQL("wisps", 1)).WithArgs(byIDsArgs(WispsFilterTables, wispIDs, true)...).WillReturnRows(emptyCountsRows())

	filter := types.IssueFilter{Labels: []string{"hot"}, SortBy: "created", Limit: 3}
	if _, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter); err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// countsRows is a counts mega-query result holding one full-hydration row
// per id, in the order given. Each row's comment count is marks[id] and its
// created_at is created[id], so a test can tell which row landed where. The
// columns are built by name from sqlbuild.IssueSelectColumns, which is what
// ScanReadyWorkRowWithCounts reads, plus the six counts columns.
func countsRows(ids []string, marks map[string]int, created map[string]time.Time) *sqlmock.Rows {
	var cols []string
	for _, c := range strings.Split(sqlbuild.IssueSelectColumns, ",") {
		cols = append(cols, strings.TrimPrefix(strings.TrimSpace(c), "leases."))
	}
	cols = append(cols, "labels_json", "dep_count", "rdep_count", "comment_count", "parent_id", "deps_json")
	rows := sqlmock.NewRows(cols)
	for _, id := range ids {
		at := created[id].UTC().Format("2006-01-02 15:04:05")
		vals := make([]driver.Value, len(cols))
		for k, c := range cols {
			switch c {
			case "id":
				vals[k] = id
			case "title":
				vals[k] = "title " + id
			case "description", "design", "acceptance_criteria", "notes":
				vals[k] = ""
			case "status":
				vals[k] = "open"
			case "priority", "compaction_level", "dep_count", "rdep_count":
				vals[k] = int64(0)
			case "issue_type":
				vals[k] = "task"
			case "created_at", "updated_at":
				vals[k] = at
			case "comment_count":
				vals[k] = int64(marks[id])
			}
		}
		rows.AddRow(vals...)
	}
	return rows
}

// scrambled returns n ids in a fixed order that is neither the order they
// sort in nor its reverse, with a distinct mark and a created_at that
// descends along that order (so a created-DESC sort keeps it).
func scrambled(prefix string, n int) (ids []string, marks map[string]int, created map[string]time.Time) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	marks, created = map[string]int{}, map[string]time.Time{}
	for k, p := range rand.New(rand.NewSource(7)).Perm(n) { //nolint:gosec // deterministic test order
		id := fmt.Sprintf("%s-%04d", prefix, p)
		ids = append(ids, id)
		marks[id] = 1000 + p
		created[id] = base.Add(-time.Duration(k) * time.Minute)
	}
	return ids, marks, created
}

// shuffled returns ids in a different fixed order, as a batch's rows arrive.
func shuffled(ids []string) []string {
	out := append([]string(nil), ids...)
	rand.New(rand.NewSource(11)).Shuffle(len(out), func(a, b int) { out[a], out[b] = out[b], out[a] }) //nolint:gosec // deterministic test order
	return out
}

// assertDelivered fails unless got holds exactly want's ids in want's order,
// each with the row the hydration returned for that id.
func assertDelivered(t *testing.T, got []*types.IssueWithCounts, want []string, marks map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("delivered %d rows, want %d", len(got), len(want))
	}
	for k, id := range want {
		if got[k].Issue.ID != id {
			t.Fatalf("row %d is %s, want %s (the id scan's order)", k, got[k].Issue.ID, id)
		}
		if got[k].CommentCount != marks[id] {
			t.Fatalf("row %d (%s) carries comment count %d, want %d: another id's row", k, id, got[k].CommentCount, marks[id])
		}
	}
}

// TestSearchIssuesWithCountsIDsFirstAtTheCeiling pins that the ceiling is
// inclusive: a bound of exactly countsIDsFirstMaxBound is still answered
// ids-first, in one by-IDs statement, and every one of its rows — the 200th
// included — is delivered, in order, though the statement returns them
// shuffled.
func TestSearchIssuesWithCountsIDsFirstAtTheCeiling(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	ids, marks, created := scrambled("bd", countsIDsFirstMaxBound)

	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).WillReturnRows(probeRow())
	mock.ExpectQuery(idScanSQL("issues", "labels", countsIDsFirstMaxBound)).WithArgs("hot").WillReturnRows(idRows(ids...))
	mock.ExpectQuery(byIDsSQL("issues", len(ids))).
		WithArgs(byIDsArgs(IssuesFilterTables, ids, true)...).WillReturnRows(countsRows(shuffled(ids), marks, created))

	filter := types.IssueFilter{Labels: []string{"hot"}, SortBy: "created", Limit: countsIDsFirstMaxBound, SkipWisps: true}
	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	assertDelivered(t, got, ids, marks)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestIDsFirstLegKeepsTheScanOrder pins the leg itself, under the merge's
// final sort: runFilterSearchQueryInTx hands back the id scan's order however
// the by-IDs statement returns its rows.
func TestIDsFirstLegKeepsTheScanOrder(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	ids, marks, created := scrambled("bw", 57)

	mock.ExpectQuery(idScanSQL("wisps", "wisp_labels", 60)).WithArgs("hot").WillReturnRows(idRows(ids...))
	mock.ExpectQuery(byIDsSQL("wisps", len(ids))).
		WithArgs(byIDsArgs(WispsFilterTables, ids, true)...).WillReturnRows(countsRows(shuffled(ids), marks, created))

	filter := types.IssueFilter{Labels: []string{"hot"}, SortBy: "created", Limit: 60}
	got, err := runFilterSearchQueryInTx(context.Background(), tx, "", filter, WispsFilterTables, true)
	if err != nil {
		t.Fatalf("runFilterSearchQueryInTx: %v", err)
	}
	assertDelivered(t, got, ids, marks)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestHydrateCountsByIDsBatches pins that the hydration binds at most
// sqlbuild.QueryBatchSize ids per statement, in the order given — 201 ids are
// two by-IDs statements, 200 ids then 1 — and returns every row in that order
// though each batch returns its rows shuffled.
func TestHydrateCountsByIDsBatches(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	ids, marks, created := scrambled("bd", sqlbuild.QueryBatchSize+1)
	first, second := ids[:sqlbuild.QueryBatchSize], ids[sqlbuild.QueryBatchSize:]

	mock.ExpectQuery(byIDsSQL("issues", len(first))).
		WithArgs(byIDsArgs(IssuesFilterTables, first, true)...).WillReturnRows(countsRows(shuffled(first), marks, created))
	mock.ExpectQuery(byIDsSQL("issues", len(second))).
		WithArgs(byIDsArgs(IssuesFilterTables, second, true)...).WillReturnRows(countsRows(second, marks, created))

	got, err := hydrateCountsByIDsInTx(context.Background(), tx, IssuesFilterTables, ids, true, sqlbuild.CountsHydration{})
	if err != nil {
		t.Fatalf("hydrateCountsByIDsInTx: %v", err)
	}
	assertDelivered(t, got, ids, marks)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestSearchIssuesWithCountsEmptyIDScanStillResolvesTables pins that a page
// the id scan finds empty still runs one by-IDs statement, over a sentinel
// id, so every table the predicate form read is still resolved — a missing
// leases or wisp_comments table fails the read as it always did (see
// counts_missing_table_test.go) — and that whatever the statement returns is
// discarded.
func TestSearchIssuesWithCountsEmptyIDScanStillResolvesTables(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).WillReturnRows(probeRow())
	mock.ExpectQuery(idScanSQL("issues", "labels", 3)).WithArgs("hot").WillReturnRows(idRows())
	mock.ExpectQuery(byIDsSQL("issues", 1)).WithArgs(byIDsArgs(IssuesFilterTables, []string{""}, true)...).
		WillReturnRows(countsRows([]string{""}, map[string]int{"": 1}, map[string]time.Time{}))

	filter := types.IssueFilter{Labels: []string{"hot"}, SortBy: "created", Limit: 3, SkipWisps: true}
	out, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d rows from an empty id scan", len(out))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestSearchIssuesWithCountsPredicateFormWhenNotIDsFirst pins the cases that
// keep the predicate form: an unbounded read (no LIMIT to push, so ids-first
// would hydrate the whole match set in batches, which measured slower), a
// Go-side sort (no SQL order to cut a page under), a bound above
// countsIDsFirstMaxBound (a MaxRows breaker with no Limit renders one), and
// a label filter with an empty entry, which the id scan's label JOINs would
// drop (labelJoinsMatchPredicate).
func TestSearchIssuesWithCountsPredicateFormWhenNotIDsFirst(t *testing.T) {
	const oneLabel = `id IN \(SELECT issue_id FROM labels WHERE label = \?\)`
	for _, tc := range []struct {
		name   string
		filter types.IssueFilter
		where  string         // the derived driver's WHERE, as a regexp
		args   []driver.Value // its args
		limit  string         // the predicate form's outer LIMIT, "" for none
	}{
		{name: "unbounded", filter: types.IssueFilter{Labels: []string{"hot"}, SortBy: "created"}},
		{name: "go-side sort", filter: types.IssueFilter{Labels: []string{"hot"}, SortBy: "id", Limit: 3}},
		{name: "bound above the ids-first ceiling", filter: types.IssueFilter{Labels: []string{"hot"}, SortBy: "created", MaxRows: countsIDsFirstMaxBound},
			limit: fmt.Sprintf("LIMIT %d", countsIDsFirstMaxBound+1)},
		{name: "empty Labels entry", filter: types.IssueFilter{Labels: []string{"hot", ""}, SortBy: "created", Limit: 3},
			where: oneLabel + ` AND ` + oneLabel, args: []driver.Value{"hot", ""}, limit: "LIMIT 3"},
		{name: "empty LabelsAny entry", filter: types.IssueFilter{LabelsAny: []string{"hot", ""}, SortBy: "created", Limit: 3},
			where: `id IN \(SELECT issue_id FROM labels WHERE label IN \(\?, \?\)\)`, args: []driver.Value{"hot", ""}, limit: "LIMIT 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).WillReturnRows(probeRow())
			// The predicate form: the filter inside the derived driver, any
			// LIMIT outside, after the aggregate joins.
			where, args := tc.where, tc.args
			if where == "" {
				where, args = oneLabel, []driver.Value{"hot"}
			}
			pred := `(?s)^\s*SELECT .*FROM \(\s*SELECT i\.\*\s+FROM issues i\s+WHERE ` + where + `\s*\) i\s.*`
			if tc.limit != "" {
				pred += regexp.QuoteMeta(tc.limit) + `\s*$`
			}
			mock.ExpectQuery(pred).WithArgs(args...).WillReturnRows(emptyCountsRows())

			f := tc.filter
			f.SkipWisps = true
			if _, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", f); err != nil {
				t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}
