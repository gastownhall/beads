package issueops

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// summaryColumnNames mirrors ScanIssueSummaryFrom's dests order exactly
// (scan.go), which in turn must agree with IssueSummaryColumns. A mismatch
// here silently misaligns every column this test inspects.
func summaryColumnNames() []string {
	return []string{
		"id", "title", "status", "priority", "issue_type", "assignee",
		"pinned", "created_at", "updated_at", "closed_at",
	}
}

// summaryMergeRow builds one full-width IssueSummaryColumns row with only
// identity/status/priority/created_at populated; assignee, pinned,
// updated_at, and closed_at are NULL. Column count and order must track
// summaryColumnNames/ScanIssueSummaryFrom.
func summaryMergeRow(id string, createdAt time.Time) []driver.Value {
	return []driver.Value{
		id, id, "open", 2, "task", nil, // id, title, status, priority, issue_type, assignee
		nil, createdAt.Format(time.RFC3339), nil, nil, // pinned, created_at, updated_at, closed_at
	}
}

// TestSearchIssueSummariesInTx_MergesAcrossIssuesAndWisps is
// TestSearchIssuesInTx_Lite_MergesAcrossIssuesAndWisps's sibling for
// summaryProjection (SearchIssueSummariesInTx): coverage for be-be3pj
// acceptance criterion 3 — wisp-merge behavior under a limit (top-N
// truncation after merge) was previously exercised only for issueProjection
// and issueLiteProjection, never for summaryProjection. summaryProjection
// already carries less: sqlbuild.LessSummary (unlike issueLiteProjection
// before be-yrtwi's fix), so this test is prophylactic: it locks in the
// currently-correct merge+trim behavior rather than reproducing a live bug.
//
// Fixture: the issues table's 2 rows are both older than either wisps-table
// row. A Limit=2 search over IssueFilter{SortBy: "created"} must return the
// true top-2 by created_at DESC — both wisp rows — not the issues-leg rows
// that would survive a naive concatenate-then-trim (always the issues leg,
// since results := append(filtered, wispResults...)). SkipLabels avoids
// mocking the per-leg label hydration queries.
func TestSearchIssueSummariesInTx_MergesAcrossIssuesAndWisps(t *testing.T) {
	t.Parallel()

	day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }

	_, mock, tx := beginMockTx(t)

	// Issues leg: SQL already orders these DESC by created_at (day5, day1) —
	// both older than anything in the wisps leg below.
	mock.ExpectQuery(`(?s)FROM issues.*LIMIT 2`).
		WillReturnRows(sqlmock.NewRows(summaryColumnNames()).
			AddRow(summaryMergeRow("bd-issue-mid", day(5))...).
			AddRow(summaryMergeRow("bd-issue-old", day(1))...))

	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	// Wisps leg: SQL already orders these DESC by created_at (day12, day10) —
	// both newer than either issues-leg row. True global top-2 by created
	// DESC is entirely this leg.
	mock.ExpectQuery(`(?s)FROM wisps.*LIMIT 2`).
		WillReturnRows(sqlmock.NewRows(summaryColumnNames()).
			AddRow(summaryMergeRow("bd-wisp-newest", day(12))...).
			AddRow(summaryMergeRow("bd-wisp-new", day(10))...))

	filter := types.IssueFilter{
		SortBy:     "created",
		Limit:      2,
		SkipLabels: true,
	}

	got, err := SearchIssueSummariesInTx(context.Background(), tx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssueSummariesInTx: %v", err)
	}

	if len(got) != 2 || got[0].ID != "bd-wisp-newest" || got[1].ID != "bd-wisp-new" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.ID
		}
		t.Fatalf("SearchIssueSummariesInTx merge: got %v, want [bd-wisp-newest bd-wisp-new] "+
			"(true created-DESC top-2 across issues+wisps, not issues-leg concatenation order)", ids)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
