package issueops

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

func TestSearchIssuesWithCountsAppliesLimitToEachSourceQuery(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM issues i.*ORDER BY i\.priority ASC, i\.created_at DESC, i\.id ASC\s+LIMIT 3`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM wisps i.*ORDER BY i\.priority ASC, i\.created_at DESC, i\.id ASC\s+LIMIT 3`).
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
	mock.ExpectQuery(`(?s)FROM issues i.*ORDER BY i\.priority ASC, i\.created_at DESC, i\.id ASC`).
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
	mock.ExpectQuery(`(?s)FROM issues i.*ORDER BY i\.created_at DESC, i\.id ASC\s+LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM wisps i.*ORDER BY i\.created_at DESC, i\.id ASC\s+LIMIT 2`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	filter := types.IssueFilter{SortBy: "created", Limit: 2}
	if _, err := SearchIssuesWithCountsInTx(context.Background(), tx, "", filter); err != nil {
		t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
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
