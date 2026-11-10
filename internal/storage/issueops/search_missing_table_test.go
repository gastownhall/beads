package issueops

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/types"
)

func boolPtr(b bool) *bool { return &b }

func tableNotFound(table string) error {
	return &mysql.MySQLError{Number: 1146, Message: "table not found: " + table}
}

type searchEntryPoint struct {
	name string
	run  func(context.Context, DBTX, string, types.IssueFilter) (int, error)
}

var searchEntryPoints = []searchEntryPoint{
	{
		name: "SearchIssuesInTx",
		run: func(ctx context.Context, tx DBTX, q string, f types.IssueFilter) (int, error) {
			out, err := SearchIssuesInTx(ctx, tx, q, f)
			return len(out), err
		},
	},
	{
		name: "SearchIssueIDsInTx",
		run: func(ctx context.Context, tx DBTX, q string, f types.IssueFilter) (int, error) {
			out, err := SearchIssueIDsInTx(ctx, tx, q, f)
			return len(out), err
		},
	},
}

// TestSearchInTxEphemeralBrokenWispPlaneIsAnError pins the tolerance to the
// wisp-plane tables a database may legitimately not have. The ephemeral branch
// runs the wisps query as its only query, so its table-not-exist check is the
// last word on what the caller is told: a table the wisp query reads but the
// wisp plane does not own -- wisp_labels during hydration, leases through
// sqlbuild.LeaseJoin -- was answered as "there are no wisps".
//
// The assertion is errors.Is against the primed driver error, not a substring
// of the message: the query text names the joined tables, so a substring check
// passes on any error that merely echoes the query.
func TestSearchInTxEphemeralBrokenWispPlaneIsAnError(t *testing.T) {
	for _, missing := range []string{"wisp_labels", "leases"} {
		for _, tc := range searchEntryPoints {
			t.Run(missing+"/"+tc.name, func(t *testing.T) {
				_, mock, tx := beginMockTx(t)
				gone := tableNotFound(missing)
				mock.ExpectQuery("FROM wisps").WillReturnError(gone)
				// Only the tolerating path reaches the issues plane. Priming it
				// makes the untightened behaviour a clean "0 rows, nil error"
				// rather than an unexpected-query error that reads like a pass.
				mock.ExpectQuery("FROM issues").WillReturnRows(sqlmock.NewRows([]string{"id"}))

				n, err := tc.run(context.Background(), tx, "", types.IssueFilter{Ephemeral: boolPtr(true)})
				if err == nil {
					t.Fatalf("search succeeded with no %s table, returning %d rows", missing, n)
				}
				if !errors.Is(err, gone) {
					t.Fatalf("error is not the missing-%s failure: %v", missing, err)
				}
			})
		}
	}
}

// TestSearchInTxEphemeralMissingWispPlaneIsEmpty is the control the tightened
// guard must keep: a pre-migration database has no wisp plane, and a search of
// it really does match no wisps. Over-tightening to "never tolerate" passes
// every must-error assertion above and fails here.
func TestSearchInTxEphemeralMissingWispPlaneIsEmpty(t *testing.T) {
	for _, table := range []string{"wisps", "wisp_dependencies"} {
		t.Run(table, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			mock.ExpectQuery("FROM wisps").WillReturnError(tableNotFound(table))
			mock.ExpectQuery("FROM issues").WillReturnRows(sqlmock.NewRows([]string{"id"}))

			out, err := SearchIssueIDsInTx(context.Background(), tx, "", types.IssueFilter{Ephemeral: boolPtr(true)})
			if err != nil {
				t.Fatalf("search errored on a database with no wisp plane: %v", err)
			}
			if len(out) != 0 {
				t.Fatalf("got %d rows, want 0", len(out))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestSearchInTxEphemeralUnrelatedErrorPropagates is the second control: the
// tolerance must not be a blanket catch in either direction.
func TestSearchInTxEphemeralUnrelatedErrorPropagates(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	boom := errors.New("connection refused")
	mock.ExpectQuery("FROM wisps").WillReturnError(boom)

	_, err := SearchIssueIDsInTx(context.Background(), tx, "", types.IssueFilter{Ephemeral: boolPtr(true)})
	if !errors.Is(err, boom) {
		t.Fatalf("search did not propagate a failed wisps query: %v", err)
	}
}

// TestSearchInTxMergeBrokenWispPlaneIsAnError covers the merge branch: the
// issues leg has already succeeded there, so the swallowed wisp failure is not
// re-raised by anything downstream and the caller gets a short list.
func TestSearchInTxMergeBrokenWispPlaneIsAnError(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	gone := tableNotFound("wisp_labels")
	mock.ExpectQuery("FROM issues").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1"))
	mock.ExpectQuery("SELECT 1 FROM wisps").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("FROM wisps").WillReturnError(gone)

	_, err := SearchIssueIDsInTx(context.Background(), tx, "", types.IssueFilter{})
	if !errors.Is(err, gone) {
		t.Fatalf("merged search hid a broken wisp plane: %v", err)
	}
}
