package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyCountsByIDsPageBindsEveryInjectionPoint pins the by-IDs counts
// mega-query's placeholder/arg alignment (gascity#7666): the guarded
// per-column reverse-blocker unions bind the page once per branch — three
// durable branches plus three wisp branches — on top of the labels, dc, cc,
// pc, deps and driver points, so a page of two ids binds 24 parameters in
// exactly the rendered order. A renderer that adds or drops a branch, or
// binds something else where a page id belongs, desyncs the placeholder and
// arg counts and fails here instead of miscounting at runtime. Which SQL
// text the branches render is pinned by sqlbuild's TestSearchCountsSQLShape;
// this test drives the real path (GetReadyWorkWithCountsInTx on a bounded
// page) so what is pinned is what production binds.
func TestReadyCountsByIDsPageBindsEveryInjectionPoint(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	// Shared probe: wisps absent (one family runs), wisp_dependencies present
	// (the reverse-blocker union carries the wisp plane), no stale flags.
	// The 4-flag probe answers wisps, wisp_dependencies and both stale-flag
	// questions in one statement, so no separate stale-blocked scan runs.
	mock.ExpectQuery(readyProbeSQL).WillReturnRows(readyProbeRows(false, true, false, false))
	mock.ExpectQuery(`SELECT id FROM issues`).WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow("pg-1").AddRow("pg-2"))

	// 12 injection points × the 2-id page, in left-to-right placeholder
	// order: labels, dc, rc durable branches 1-3, rc wisp branches 1-3, cc,
	// pc, d, driver.
	page := []driver.Value{"pg-1", "pg-2"}
	wantArgs := make([]driver.Value, 0, 12*2)
	for range 12 {
		wantArgs = append(wantArgs, page...)
	}
	// The by-IDs driver clause identifies the mega-query: nothing else on
	// this path carries WHERE i.id IN (?,?).
	mock.ExpectQuery(`WHERE i\.id IN \(\?,\?\)`).
		WithArgs(wantArgs...).
		WillReturnRows(emptyCountsRows())

	out, err := GetReadyWorkWithCountsInTx(context.Background(), tx, types.WorkFilter{IncludeDeferred: true, Limit: 5})
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsInTx on a bounded page: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("GetReadyWorkWithCountsInTx = %d rows, want 0 (empty hydration)", len(out))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (placeholder/arg order drifted?): %v", err)
	}
}

// TestReadyCountsByIDsPageBindsArgsWithoutWispDeps is the dropped-wisp-plane
// twin: a working-set reset that dropped wisp_dependencies makes the one-shot
// probe fail per-table, the fallback probes each table on its own, and the
// reverse-blocker then carries only the durable plane's three branches — 9
// injection points. This is also the missing-plane tolerance in action: the
// page hydrates over the durable plane alone instead of erroring.
func TestReadyCountsByIDsPageBindsArgsWithoutWispDeps(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	// One-shot probe dies on the missing table; the per-table fallback then
	// probes wisp_dependencies (missing), wisps (empty), and reads the issues
	// stale set unconditionally.
	mock.ExpectQuery(readyProbeSQL).WillReturnError(tableNotFound("wisp_dependencies"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisp_dependencies LIMIT 1")).
		WillReturnError(tableNotFound("wisp_dependencies"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(staleBlockedQueryRegex("issues")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT id FROM issues`).WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow("pg-9"))

	page := []driver.Value{"pg-9"}
	wantArgs := make([]driver.Value, 0, 9)
	for range 9 {
		wantArgs = append(wantArgs, page...)
	}
	mock.ExpectQuery(`WHERE i\.id IN \(\?\)`).
		WithArgs(wantArgs...).
		WillReturnRows(emptyCountsRows())

	if _, err := GetReadyWorkWithCountsInTx(context.Background(), tx, types.WorkFilter{IncludeDeferred: true, Limit: 5}); err != nil {
		t.Fatalf("GetReadyWorkWithCountsInTx without the wisp plane: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
