package issueops

import (
	"context"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin GH#4370's hydration shape: a counts read selects its ordered
// IDs first and then hydrates exactly those IDs through the by-IDs form of
// sqlbuild.SearchCountsSQL, in sqlbuild.QueryBatchSize batches. sqlmock runs
// in ordered mode and rejects any statement without a matching expectation,
// so the predicate-form mega-query (whose aggregate subqueries read their
// whole side tables) cannot run without failing the test.
//
// Every fixture uses Lite hydration so the hydrated rows can reuse
// liteColumnNames/liteMergeRow (search_lite_merge_test.go); the batching and
// reordering under test do not depend on the projection width.

// countsHydrationColumns is the lite counts-row shape ScanReadyWorkRowWithCounts
// reads: the lite issue columns followed by the six aggregate columns.
func countsHydrationColumns() []string {
	return append(liteColumnNames(), "labels_json", "dep_count", "rdep_count", "comment_count", "parent_id", "deps_json")
}

// hydratedRowsReversed returns counts rows for ids in REVERSE order, so a
// caller that forgot to restore the selection order is caught.
func hydratedRowsReversed(ids []string) *sqlmock.Rows {
	rows := sqlmock.NewRows(countsHydrationColumns())
	created := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := len(ids) - 1; i >= 0; i-- {
		vals := append(liteMergeRow(ids[i], created), nil, 0, 0, 0, nil, nil)
		rows.AddRow(vals...)
	}
	return rows
}

func seqIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		// Zero-padded so byte order, natural order and selection order agree.
		ids[i] = fmt.Sprintf("bd-%04d", i)
	}
	return ids
}

// expectByIDsHydration registers the exact by-IDs statement and argument list
// SearchCountsSQL renders for one batch, answering with that batch reversed.
func expectByIDsHydration(mock sqlmock.Sqlmock, tables FilterTables, batch []string, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) {
	query, args := sqlbuild.SearchCountsSQL(tables, batch, "", "", "", includeWispReverseDeps, hyd)
	want := make([]driver.Value, len(args))
	for i, a := range args {
		want[i] = a
	}
	mock.ExpectQuery("^" + regexp.QuoteMeta(strings.TrimSpace(query)) + "$").
		WithArgs(want...).
		WillReturnRows(hydratedRowsReversed(batch))
}

func gotIDs(items []*types.IssueWithCounts) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Issue.ID
	}
	return out
}

func assertIDs(t *testing.T, got []*types.IssueWithCounts, want []string) {
	t.Helper()
	ids := gotIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("got %d rows, want %d", len(ids), len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (order not restored from the ID selection)", i, ids[i], want[i])
		}
	}
}

// TestSearchIssuesWithCountsHydratesOnlyThePageIDs (AC-1): a bounded,
// SQL-sorted counts search runs the ordered, LIMIT-bound id-only query first
// and then hydrates exactly the selected IDs; the predicate-form counts query
// never runs. An empty selection hydrates nothing.
func TestSearchIssuesWithCountsHydratesOnlyThePageIDs(t *testing.T) {
	t.Parallel()

	hyd := sqlbuild.CountsHydration{Lite: true}
	filter := types.IssueFilter{Limit: 3, SkipWisps: true, Lite: true}
	idQuery := `(?s)^SELECT issues\.id FROM issues .*ORDER BY priority ASC, created_at DESC, id ASC\s+LIMIT 3\s*$`

	t.Run("page", func(t *testing.T) {
		t.Parallel()
		page := []string{"bd-a", "bd-b", "bd-c"}

		_, mock, tx := beginMockTx(t)
		mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		mock.ExpectQuery(idQuery).WillReturnRows(idRows(page...))
		expectByIDsHydration(mock, IssuesFilterTables, page, true, hyd)

		got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
		}
		assertIDs(t, got, page)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		_, mock, tx := beginMockTx(t)
		mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		mock.ExpectQuery(idQuery).WillReturnRows(idRows())

		got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter)
		if err != nil {
			t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d rows from an empty selection, want none", len(got))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

// TestSearchIssuesWithCountsUnboundedHydratesInBatches (AC-2): an unlimited
// counts search selects every matching ID with no LIMIT, then hydrates them
// in batches of 200 and 5, returning all 205 in selection order.
func TestSearchIssuesWithCountsUnboundedHydratesInBatches(t *testing.T) {
	t.Parallel()

	ids := seqIDs(205)
	hyd := sqlbuild.CountsHydration{Lite: true}

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)^SELECT issues\.id FROM issues .*ORDER BY priority ASC, created_at DESC, id ASC\s*$`).
		WillReturnRows(idRows(ids...))
	expectByIDsHydration(mock, IssuesFilterTables, ids[:sqlbuild.QueryBatchSize], true, hyd)
	expectByIDsHydration(mock, IssuesFilterTables, ids[sqlbuild.QueryBatchSize:], true, hyd)

	got, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", types.IssueFilter{SkipWisps: true, Lite: true})
	if err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	assertIDs(t, got, ids)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestReadyWorkWithCountsUnboundedHydratesByIDs (AC-2): an unlimited ready
// family read runs its indexed ID query without a LIMIT and hydrates the IDs
// in batches of 200 and 5 — it no longer falls back to the predicate-form
// mega-query — and its total is the number of selected IDs. An empty
// selection hydrates nothing.
func TestReadyWorkWithCountsUnboundedHydratesByIDs(t *testing.T) {
	t.Parallel()

	hyd := sqlbuild.CountsHydration{Lite: true}
	preds := &readyWorkPredicates{
		whereSQL:   "WHERE status = ?",
		orderBySQL: "ORDER BY priority ASC, created_at ASC, id ASC",
		args:       []interface{}{"open"},
	}
	idQuery := `^SELECT id FROM issues WHERE status = \? ORDER BY priority ASC, created_at ASC, id ASC\s*$`

	t.Run("batched", func(t *testing.T) {
		t.Parallel()
		ids := seqIDs(205)

		_, mock, tx := beginMockTx(t)
		mock.ExpectQuery(idQuery).WithArgs("open").WillReturnRows(idRows(ids...))
		expectByIDsHydration(mock, IssuesFilterTables, ids[:sqlbuild.QueryBatchSize], true, hyd)
		expectByIDsHydration(mock, IssuesFilterTables, ids[sqlbuild.QueryBatchSize:], true, hyd)

		got, total, err := runReadyCountsInTx(context.Background(), tx, IssuesFilterTables, 0, preds, true, hyd, false)
		if err != nil {
			t.Fatalf("runReadyCountsInTx: %v", err)
		}
		assertIDs(t, got, ids)
		if total != len(ids) {
			t.Fatalf("total = %d, want %d", total, len(ids))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		_, mock, tx := beginMockTx(t)
		mock.ExpectQuery(idQuery).WithArgs("open").WillReturnRows(idRows())

		got, total, err := runReadyCountsInTx(context.Background(), tx, IssuesFilterTables, 0, preds, true, hyd, false)
		if err != nil {
			t.Fatalf("runReadyCountsInTx: %v", err)
		}
		if len(got) != 0 || total != 0 {
			t.Fatalf("empty selection returned %d rows, total %d; want none", len(got), total)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}
