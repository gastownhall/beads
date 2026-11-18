package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func TestSearchIssuesWithCountsAppliesLimitToEachSourceQuery(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY priority ASC, created_at DESC, id ASC\s+LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT wisps\.id FROM wisps.*ORDER BY priority ASC, created_at DESC, id ASC\s+LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", types.IssueFilter{Limit: 3})
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SearchIssuesWithCountsInTx returned %d rows, want none", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Regression for bd-6dnrw.42: SkipWisps must suppress the wisps probe and
// merge, matching SearchIssuesInTx and the domain/db counts path. sqlmock
// rejects any query without a matching expectation, so only the
// wisp_dependencies probe and the issues query may run.
func TestSearchIssuesWithCountsHonorsSkipWisps(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY priority ASC, created_at DESC, id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", types.IssueFilter{SkipWisps: true})
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SearchIssuesWithCountsInTx returned %d rows, want none", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Regression for bd-6dnrw.43: SortBy/SortDesc must reach the per-table SQL
// ORDER BY; otherwise LIMIT is applied under priority order and the wrong
// row SET survives (not just the wrong order).
func TestSearchIssuesWithCountsPushesSortIntoSQL(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY created_at DESC, id ASC\s+LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT wisps\.id FROM wisps.*ORDER BY created_at DESC, id ASC\s+LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	filter := types.IssueFilter{SortBy: "created", Limit: 2}
	if _, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter); err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// countsMockRow builds one row of the by-IDs counts mega-query for
// ScanIssueFrom's positional dest list: IssueSelectColumns (issue columns plus
// the 3 lease columns) followed by labels_json, dep/rdep/comment counts,
// parent_id and deps_json. TestSearchCountsByIDsRowWidth pins the width to
// that projection. Only the direct (non-NULL) dests carry values; everything
// else scans as NULL.
func countsMockRow(id, title string, labelsJSON string, depCount, rdepCount, commentCount int) []driver.Value {
	row := make([]driver.Value, 59)
	row[0] = id                     // id
	row[2] = title                  // title
	row[3] = ""                     // description (direct string dest)
	row[4] = ""                     // design
	row[5] = ""                     // acceptance_criteria
	row[6] = ""                     // notes
	row[7] = "open"                 // status
	row[8] = 2                      // priority
	row[9] = "task"                 // issue_type
	row[12] = "2026-01-02 03:04:05" // created_at (string, parsed in Go)
	row[15] = "2026-01-02 03:04:05" // updated_at
	row[20] = 0                     // compaction_level
	row[53] = labelsJSON            // labels_json
	row[54] = int64(depCount)       // dep_count
	row[55] = int64(rdepCount)      // rdep_count
	row[56] = int64(commentCount)   // comment_count
	// row[58] (deps_json) stays NULL except where a test sets it.
	return row
}

// TestSearchCountsByIDsRowWidth pins the by-IDs row used by countsMockRow to
// the projection the by-IDs counts form actually scans. A migration that adds
// or drops a column shifts every hand-built row by one, and this fails with an
// actionable message instead of a mysterious scan panic in another test.
func TestSearchCountsByIDsRowWidth(t *testing.T) {
	t.Parallel()

	want := len(sqlbuild.ColumnNames(IssueSelectColumns)) + 6
	got := len(countsMockRow("bd-x", "t", "[]", 1, 2, 3))
	if want != got {
		t.Fatalf("countsMockRow width = %d, want %d (IssueSelectColumns + 6 extras): reconcile countsMockRow with the by-IDs projection", got, want)
	}
}

// countsMockRows assembles full rows (one per id) in the given order.
func countsMockRows(ids []string) *sqlmock.Rows {
	cols := make([]string, 59)
	for i := range cols {
		cols[i] = fmt.Sprintf("c%d", i)
	}
	rows := sqlmock.NewRows(cols)
	for i, id := range ids {
		row := countsMockRow(id, "row "+id, `["alpha"]`, i, i+1, i+2)
		rows.AddRow(row...)
	}
	return rows
}

// beginCapturingCountsMockTx is beginMockTx with a query matcher that also
// records every actual SQL string, so a test can assert about query shapes —
// which form ran, how many queries ran — rather than only presence.
func beginCapturingCountsMockTx(t *testing.T, captured *[]string) (sqlmock.Sqlmock, DBTX) {
	t.Helper()

	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		*captured = append(*captured, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	return mock, tx
}

// TestSearchIssuesWithCountsHydratesOnlyThePageIDs is the GH#4370 AC-1 pin: a
// bounded SQL-sort counts request must first run the ordered, LIMIT-bound ID
// selection and then hydrate ONLY that page through the by-IDs counts form —
// never the predicate-form counts query whose aggregate LEFT JOINs read the
// whole side tables. Four probes of that:
//
//   - the ID query carries the page bound (ORDER BY ... LIMIT 3);
//   - exactly one counts query follows it;
//   - that query is the by-IDs form ("WHERE i.id IN (?,?,?)" — exactly the 3
//     page IDs, so a page-sized hydration, not a set-sized one); and
//   - no captured query carries the predicate form's derived driver
//     ("SELECT i.*"), which is how the two forms are told apart at all.
//
// The hydration rows come back in a different order than the page did; the
// delivered page must be the ID query's (SQL-ordered) page either way, and the
// terminal finishSearchIssuesWithCounts sort keeps asserting it afterwards.
func TestSearchIssuesWithCountsHydratesOnlyThePageIDs(t *testing.T) {
	t.Parallel()

	var captured []string
	mock, tx := beginCapturingCountsMockTx(t, &captured)

	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY priority ASC, created_at DESC, id ASC\s+LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow("bd-1").AddRow("bd-2").AddRow("bd-3"))
	mock.ExpectQuery(`WHERE i\.id IN \(\?,\?,\?\)`).
		WillReturnRows(countsMockRows([]string{"bd-3", "bd-1", "bd-2"}))

	txSQL, ok := tx.(*sql.Tx)
	if !ok {
		t.Fatal("beginCapturingCountsMockTx must hand back a *sql.Tx for the counts seam")
	}
	got, err := SearchIssuesWithCountsInTx(context.Background(), txSQL, "", types.IssueFilter{Limit: 3, SkipWisps: true})
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(got) != 3 || got[0].Issue.ID != "bd-1" || got[1].Issue.ID != "bd-2" || got[2].Issue.ID != "bd-3" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.Issue.ID
		}
		t.Fatalf("page = %v, want [bd-1 bd-2 bd-3] (the ID query's order)", ids)
	}
	if got[0].DependencyCount != 1 || got[0].DependentCount != 2 || got[0].CommentCount != 3 {
		// bd-1 sat at hydration position 1 of [bd-3 bd-1 bd-2], so it carried
		// (1, 2, 3) — proving the delivered row kept its own hydrated values
		// while landing on the page in the ID query's order.
		t.Fatalf("hydrated counts for bd-1 = %+v, want hydration-position values (1,2,3)", got[0])
	}
	if len(got[2].Issue.Labels) != 1 || got[2].Issue.Labels[0] != "alpha" {
		t.Fatalf("labels not hydrated: %v", got[2].Issue.Labels)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}

	if len(captured) != 3 {
		t.Fatalf("query count = %d, want 3 (probe, ID select, one by-IDs hydration)", len(captured))
	}
	for _, q := range captured {
		if strings.Contains(q, "SELECT i.*") {
			t.Fatalf("a predicate-form counts query ran (derived driver SELECT i.*): %s", q)
		}
	}
}

// TestSearchIssuesWithCountsEmptyPageSkipsTheCountsQuery: an empty ID page must
// answer without ANY counts query. The by-IDs form is selected by "ids
// non-empty"; a hydration handed zero IDs has to refuse to fall back to the
// predicate form, whose aggregates would read the whole side tables for
// nothing (GH#4370).
func TestSearchIssuesWithCountsEmptyPageSkipsTheCountsQuery(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY priority ASC, created_at DESC, id ASC\s+LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	// No counts-query expectation: sqlmock fails the test if one runs.

	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", types.IssueFilter{Limit: 3, SkipWisps: true})
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SearchIssuesWithCountsInTx returned %d rows, want none", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (or an unexpected counts query ran): %v", err)
	}
}

// Regression for bd-6dnrw.43 (merge half): when issues and wisps results are
// merged before the limit cut, the in-memory sort must follow the requested
// SortBy, or the surviving row set reverts to priority order.
func TestFinishSearchIssuesWithCountsTruncatesByRequestedSort(t *testing.T) {
	t.Parallel()

	at := func(day int) time.Time {
		return time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)
	}
	iwc := func(id string, priority int, created time.Time) *types.IssueWithCounts {
		return &types.IssueWithCounts{Issue: &types.Issue{ID: id, Priority: priority, CreatedAt: created}}
	}
	// P0 but oldest; the two newest are low priority.
	items := []*types.IssueWithCounts{
		iwc("bd-old-p0", 0, at(1)),
		iwc("bd-new-p3", 3, at(9)),
		iwc("bd-mid-p2", 2, at(5)),
		iwc("bd-newest-p4", 4, at(10)),
	}

	got, err := finishSearchIssuesWithCounts(items, types.IssueFilter{SortBy: "created", Limit: 2})
	if err != nil {
		t.Fatalf("sort=created limit=2: unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Issue.ID != "bd-newest-p4" || got[1].Issue.ID != "bd-new-p3" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.Issue.ID
		}
		t.Fatalf("sort=created limit=2 kept %v, want [bd-newest-p4 bd-new-p3]", ids)
	}

	// SortDesc flips the per-key default direction: created becomes ASC.
	got, err = finishSearchIssuesWithCounts(items, types.IssueFilter{SortBy: "created", SortDesc: true, Limit: 2})
	if err != nil {
		t.Fatalf("sort=created desc limit=2: unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Issue.ID != "bd-old-p0" || got[1].Issue.ID != "bd-mid-p2" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.Issue.ID
		}
		t.Fatalf("sort=created desc limit=2 kept %v, want [bd-old-p0 bd-mid-p2]", ids)
	}

	// Default (no SortBy) keeps the historical priority/created/id order.
	got, err = finishSearchIssuesWithCounts(items, types.IssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("default sort limit=2: unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Issue.ID != "bd-old-p0" || got[1].Issue.ID != "bd-mid-p2" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.Issue.ID
		}
		t.Fatalf("default sort limit=2 kept %v, want [bd-old-p0 bd-mid-p2]", ids)
	}
}
