package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
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

// TestExecuteCloseBatchWithPolicyDefersToTheTargetRead pins the external
// policy's fallback read. A snapshot names only ids, so it refuses a live
// target alone: a missing target keeps the backend's not-found, and a status
// read that fails reports that failure instead of a policy refusal.
func TestExecuteCloseBatchWithPolicyDefersToTheTargetRead(t *testing.T) {
	t.Parallel()

	const id = "close-policy-target"
	readErr := errors.New("status read failed")
	statusQuery := func(table string) string { return `SELECT status FROM ` + table + ` WHERE id = \?` }
	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		want   error
	}{
		{
			// Neither table has the row, for the policy's read or for
			// ExecuteClose's own, so the backend's not-found stands.
			name: "missing",
			expect: func(mock sqlmock.Sqlmock) {
				for range 2 {
					for _, table := range []string{"issues", "wisps"} {
						mock.ExpectQuery(statusQuery(table)).
							WithArgs(id).
							WillReturnRows(sqlmock.NewRows([]string{"status"}))
					}
				}
			},
			want: storage.ErrNotFound,
		},
		{
			name: "unreadable",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(statusQuery("issues")).
					WithArgs(id).
					WillReturnError(readErr)
			},
			want: readErr,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, mock, tx := beginMockTx(t)
			tc.expect(mock)
			result, _, err := ExecuteCloseBatchWithPolicy(context.Background(), tx, publicops.CloseBatchRequest{
				Actor: "tester",
				Items: []publicops.BatchCloseItem{{IssueID: id}},
			}, nil, storage.NewBatchClosePolicy(map[string][]string{id: {"external:p:c"}}))
			if err != nil {
				t.Fatalf("ExecuteCloseBatchWithPolicy: %v", err)
			}
			if got := result.Outcomes[0].Err; !errors.Is(got, tc.want) || errors.Is(got, storage.ErrCloseBlocked) {
				t.Fatalf("outcome error = %v, want %v rather than a policy refusal", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet target-read SQL expectations: %v", err)
			}
		})
	}
}
