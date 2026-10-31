package db

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetReadyWorkIDPageWalksTheParentDescendantsOnce pins that a scoped ready
// page computes the parent's transitive descendants once and reuses them for
// both planes. The union used to build its per-plane predicates independently,
// running the descendant walk — the dominant cost of `bd ready --parent` — and
// the deferred-parent probes once for issues and again for wisps (be-qfm).
// The script below is the whole allowed sequence: one deferred probe per issue
// table, one walk, the wisps-plane probe, the union. A second walk or probe
// before the union is an unexpected query and fails the test.
func TestGetReadyWorkIDPageWalksTheParentDescendantsOnce(t *testing.T) {
	t.Parallel()

	mock, repo := newMockRepo(t)
	parent := "rw-parent"

	mock.ExpectQuery(`SELECT 1 FROM issues WHERE defer_until IS NOT NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps WHERE defer_until IS NOT NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(`WITH RECURSIVE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "depth"}).AddRow("rw-child", 1))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`SELECT id, src FROM`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "src"}).AddRow("rw-child", "i"))

	page, _, err := repo.getReadyWorkIDPage(context.Background(), types.WorkFilter{ParentID: &parent, Limit: 10})
	if err != nil {
		t.Fatalf("getReadyWorkIDPage: %v", err)
	}
	if len(page.issueIDs) != 1 || page.issueIDs[0] != "rw-child" {
		t.Fatalf("page issue ids = %v, want [rw-child]", page.issueIDs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected query sequence: %v", err)
	}
}
