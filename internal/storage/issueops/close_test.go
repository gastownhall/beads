package issueops

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestCountOpenChildrenInTxUsesTargetedOpenEdges keeps the close guard from
// regressing to a dependent-record scan followed by full issue hydration. The
// count needs only two indexed aggregates: permanent children and wisp children
// whose natural edge is not already durable.
func TestCountOpenChildrenInTxUsesTargetedOpenEdges(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	const parent = "close-count-parent"

	// Route the target durable-first, then use direct typed-target predicates
	// (not COALESCE), literal closed filtering, and a durable-id anti-join for
	// the wisp aggregate. The regexes intentionally leave SQL layout free.
	mock.ExpectQuery(`SELECT 1 FROM issues WHERE id = \?`).
		WithArgs(parent).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	durableQuery := `(?s)SELECT\s+COUNT\(DISTINCT\s+dependency\.issue_id\).*FROM\s+dependencies.*JOIN\s+issues.*depends_on_issue_id\s*=\s*\?.*type\s*=\s*'parent-child'.*status\s*!=\s*'closed'`
	wispQuery := `(?s)SELECT\s+COUNT\(DISTINCT\s+dependency\.issue_id\).*FROM\s+wisp_dependencies.*JOIN\s+wisps.*depends_on_issue_id\s*=\s*\?.*type\s*=\s*'parent-child'.*status\s*!=\s*'closed'.*NOT EXISTS.*FROM\s+dependencies.*durable\.id\s*=\s*dependency\.id`
	mock.ExpectQuery(durableQuery).
		WithArgs(parent).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(wispQuery).
		WithArgs(parent).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	got, err := countOpenChildrenInTx(context.Background(), tx, parent)
	if err != nil {
		t.Fatalf("countOpenChildrenInTx: %v", err)
	}
	if got != 2 {
		t.Fatalf("open child count = %d, want 2 (one durable + one wisp parent-child edge)", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet targeted-count SQL expectations: %v", err)
	}
}
