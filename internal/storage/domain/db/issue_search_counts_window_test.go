package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// countsMockRow builds one row of the by-IDs counts mega-query for
// issueops.ScanReadyWorkRowWithCounts's positional dest list: 50 issue columns,
// 3 lease columns, then labels_json, dep/rdep/comment counts, parent_id and
// deps_json. TestSearchCountsByIDsRowWidthDb pins the width to that projection.
func countsMockRow(id, title string, depCount, rdepCount, commentCount int) []driver.Value {
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
	row[53] = `["alpha"]`           // labels_json
	row[54] = int64(depCount)       // dep_count
	row[55] = int64(rdepCount)      // rdep_count
	row[56] = int64(commentCount)   // comment_count
	return row
}

func TestSearchCountsByIDsRowWidthDb(t *testing.T) {
	want := len(sqlbuild.ColumnNames(issueSelectColumns)) + 6
	got := len(countsMockRow("bd-x", "t", 1, 2, 3))
	if want != got {
		t.Fatalf("countsMockRow width = %d, want %d (issueSelectColumns + 6 extras): reconcile countsMockRow with the by-IDs projection", got, want)
	}
}

func countsMockRows(ids []string) *sqlmock.Rows {
	cols := make([]string, 59)
	for i := range cols {
		cols[i] = fmt.Sprintf("c%d", i)
	}
	rows := sqlmock.NewRows(cols)
	for i, id := range ids {
		rows.AddRow(countsMockRow(id, "row "+id, i, i+1, i+2)...)
	}
	return rows
}

func idRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

// newCapturingMockRepo is newMockRepo whose query matcher records every actual
// SQL string, so a test can assert which query forms ran (the by-IDs counts
// form is told apart from the predicate form by "WHERE i.id IN" vs the derived
// driver "SELECT i.*").
func newCapturingMockRepo(t *testing.T, captured *[]string) (sqlmock.Sqlmock, *issueSQLRepositoryImpl) {
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
	return mock, &issueSQLRepositoryImpl{runner: db}
}

// wispProbeEmpty primes `SELECT 1 FROM wisps LIMIT 1` to answer ErrNoRows — the
// empty-wisps plane — after the wisp_dependencies existence probe.
func wispProbeEmpty(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
}

func noPredicateCountsQuery(t *testing.T, captured []string) {
	t.Helper()
	for _, q := range captured {
		if strings.Contains(q, "SELECT i.*") {
			t.Fatalf("a predicate-form counts query ran (derived driver SELECT i.*): %s", q)
		}
	}
}

// TestSearchCountsNonUnionSelectsIDsFirst is the GH#4370 AC-5 pin: every
// NON-UNION proxied counts branch (SkipWisps, and the empty/missing wisps
// probe route) must select its ID window first — cap, offset and the HasMore
// probe row resolved once, on the IDs — and then hydrate only those IDs through
// the by-IDs counts form, never the predicate-form counts query whose
// aggregates read the side tables unconstrained.
func TestSearchCountsNonUnionSelectsIDsFirst(t *testing.T) {
	t.Run("BoundedPageHydratesOnlyTheWindow", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		wispProbeEmpty(mock)
		// limit 2: the window carries probe row +1, so LIMIT 3 in SQL and a
		// 2-row page with HasMore.
		mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY issues\.priority ASC, issues\.created_at DESC, issues\.id ASC LIMIT 3`).
			WillReturnRows(idRows("bd-1", "bd-2", "bd-3"))
		mock.ExpectQuery(`WHERE i\.id IN \(\?,\?\)`).
			WillReturnRows(countsMockRows([]string{"bd-2", "bd-1"}))

		page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{Limit: 2})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		got := iwcIDsOf(page)
		if len(got) != 2 || got[0] != "bd-1" || got[1] != "bd-2" {
			t.Fatalf("page = %v, want [bd-1 bd-2]", got)
		}
		if !page.HasMore {
			t.Fatalf("HasMore = false, want the probe row's verdict true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
		noPredicateCountsQuery(t, captured)
		if len(captured) != 4 {
			t.Fatalf("query count = %d, want 4 (deps probe, wisps probe, ID window, by-IDs hydration)", len(captured))
		}
	})

	t.Run("OffsetRidesTheWindowOnce", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		wispProbeEmpty(mock)
		// limit 2, offset 1: the engine carries the offset (LIMIT 3 OFFSET 1)
		// and the Go half of the window must NOT skip again.
		mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY issues\.priority ASC, issues\.created_at DESC, issues\.id ASC LIMIT 3 OFFSET 1`).
			WillReturnRows(idRows("bd-2", "bd-3", "bd-4"))
		mock.ExpectQuery(`WHERE i\.id IN \(\?,\?\)`).
			WillReturnRows(countsMockRows([]string{"bd-3", "bd-2"}))

		page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{Limit: 2, Offset: 1})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		got := iwcIDsOf(page)
		if len(got) != 2 || got[0] != "bd-2" || got[1] != "bd-3" {
			t.Fatalf("page = %v, want [bd-2 bd-3] (offset applied exactly once)", got)
		}
		if !page.HasMore {
			t.Fatalf("HasMore = false, want true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("CapFiresOnTheIDsTheQueryTouched", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		wispProbeEmpty(mock)
		// limit 2, maxRows 1: the window sizes LIMIT 2 (cap+1) and the cap
		// fires on the 2 IDs the query touched, BEFORE any hydration — the
		// caller is told the result set walked past the breaker, not handed
		// a truncated page.
		mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*LIMIT 2`).
			WillReturnRows(idRows("bd-1", "bd-2"))

		_, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{Limit: 2, MaxRows: 1, MaxRowsSource: "ac-0 test"})
		if err == nil {
			t.Fatalf("max-rows overage was not refused")
		}
		var tooMany *issueops.ErrTooManyRows
		if !errors.As(err, &tooMany) {
			t.Fatalf("overage error = %v, want ErrTooManyRows", err)
		}
		if tooMany.Found != 2 || tooMany.Cap != 1 {
			t.Fatalf("cap accounting = found %d cap %d, want found 2 cap 1", tooMany.Found, tooMany.Cap)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
		for _, q := range captured {
			if strings.Contains(q, "WHERE i.id IN") {
				t.Fatalf("hydration ran before the cap refused the page: %s", q)
			}
		}
	})

	t.Run("UnboundedHydratesInBatches", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		wispProbeEmpty(mock)
		ids := make([]string, 205)
		all := sqlmock.NewRows([]string{"id"})
		for i := range ids {
			ids[i] = fmt.Sprintf("bd-%03d", i)
			all.AddRow(ids[i])
		}
		// Limit 0 and no cap: no SQL bound; the Go window must not trim the
		// set, and hydration chunks at the batch size.
		mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues`).
			WillReturnRows(all)
		mock.ExpectQuery(`WHERE i\.id IN \(\?(?:,\?){199}\)`).
			WillReturnRows(countsMockRows(ids[:200]))
		mock.ExpectQuery(`WHERE i\.id IN \(\?(?:,\?){4}\)`).
			WillReturnRows(countsMockRows(ids[200:]))

		page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{Limit: 0})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		got := iwcIDsOf(page)
		if len(got) != 205 {
			t.Fatalf("row count = %d, want 205", len(got))
		}
		for i := range ids {
			if got[i] != ids[i] {
				t.Fatalf("row %d = %s, want %s", i, got[i], ids[i])
			}
		}
		if page.HasMore {
			t.Fatalf("HasMore = true on an unbounded page, want false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
		noPredicateCountsQuery(t, captured)
	})

	t.Run("GoSideSortOrdersTheIDsInGo", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		wispProbeEmpty(mock)
		// sort id renders no ORDER BY and the window pushes no bound under a
		// Go-side key; the complete matching set arrives and Go orders it
		// before the member-of-window trim.
		mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues\s*$`).
			WillReturnRows(idRows("bd-4", "bd-1", "bd-9", "bd-2"))
		mock.ExpectQuery(`WHERE i\.id IN \(\?,\?\)`).
			WillReturnRows(countsMockRows([]string{"bd-1", "bd-2"}))

		page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{SortBy: "id", Limit: 2})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		got := iwcIDsOf(page)
		if len(got) != 2 || got[0] != "bd-1" || got[1] != "bd-2" {
			t.Fatalf("page = %v, want [bd-1 bd-2] (byte order, then trim)", got)
		}
		if !page.HasMore {
			t.Fatalf("HasMore = false, want true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("NoIDShrinkMustNotRestoreThePredicateForm", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		wispProbeEmpty(mock)
		// NoIDShrink opts the PLAIN seam out of the id-shrink route, where
		// the alternative is a wide SELECT; for the counts seam the
		// alternative is the predicate-form counts query, so the flag must
		// not restore it.
		mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues`).
			WillReturnRows(idRows("bd-1"))
		mock.ExpectQuery(`WHERE i\.id IN \(\?\)`).
			WillReturnRows(countsMockRows([]string{"bd-1"}))

		page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{Limit: 1, NoIDShrink: true})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		if got := iwcIDsOf(page); len(got) != 1 || got[0] != "bd-1" {
			t.Fatalf("page = %v, want [bd-1]", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
		noPredicateCountsQuery(t, captured)
	})

	t.Run("SkipWispsRoute", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		// SkipWisps suppresses the wisps probe entirely; only the
		// wisp_dependencies existence probe runs (it is read before the
		// routing check).
		mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		mock.ExpectQuery(`(?s)SELECT issues\.id FROM issues.*ORDER BY issues\.priority ASC, issues\.created_at DESC, issues\.id ASC LIMIT 4`).
			WillReturnRows(idRows("bd-1", "bd-2"))
		mock.ExpectQuery(`WHERE i\.id IN \(\?,\?\)`).
			WillReturnRows(countsMockRows([]string{"bd-1", "bd-2"}))

		page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{Limit: 3, SkipWisps: true})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		if got := iwcIDsOf(page); len(got) != 2 || got[0] != "bd-1" || got[1] != "bd-2" {
			t.Fatalf("page = %v, want [bd-1 bd-2]", got)
		}
		if page.HasMore {
			t.Fatalf("HasMore = true on an exact page, want false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("EphemeralOnlyRouteAnswersWispsOnly", func(t *testing.T) {
		var captured []string
		mock, repo := newCapturingMockRepo(t, &captured)
		mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
			WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
		mock.ExpectQuery(`(?s)SELECT wisps\.id FROM wisps`).
			WillReturnRows(idRows("bd-w1"))
		mock.ExpectQuery(`WHERE i\.id IN \(\?\)`).
			WillReturnRows(countsMockRows([]string{"bd-w1"}))

		eph := true
		page, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "",
			types.IssueFilter{Ephemeral: &eph})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		if got := iwcIDsOf(page); len(got) != 1 || got[0] != "bd-w1" {
			t.Fatalf("page = %v, want [bd-w1]", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})
}

func iwcIDsOf(page domain.SearchCountsPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.Issue.ID)
	}
	return out
}
