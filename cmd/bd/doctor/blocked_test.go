package doctor

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectStaleBlockedCount answers the stale-flag count of the Blocked State
// check (issueops.CountIsBlockedInconsistenciesInTx) over a database with no
// dangling wisp edge and no close gate: the two holder probes, the two
// explained-parent reads, and one count per table, the issues count giving
// stale.
func expectStaleBlockedCount(mock sqlmock.Sqlmock, stale int) {
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		mock.ExpectQuery(regexp.QuoteMeta("d.issue_id FROM " + depTable + " d")).
			WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	}
	for range 2 {
		mock.ExpectQuery(`WITH\s+cand\(pid\)`).
			WillReturnRows(sqlmock.NewRows([]string{"pid", "dangling"}))
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM issues i")).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(stale))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM wisps w")).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
}

// TestCheckBlockedConsistencyDB_ReportsAFailedUndecidedCount: a count of the
// rows left as found that could not be read is not a count of none. The check
// keeps its verdict, which rests on the stale count, and says in Detail that
// the other count is missing, on both branches.
func TestCheckBlockedConsistencyDB_ReportsAFailedUndecidedCount(t *testing.T) {
	readErr := errors.New("read timed out")
	for _, tc := range []struct {
		name       string
		stale      int
		wantStatus string
		wantDetail string
	}{
		{"consistent", 0, StatusOK, ""},
		{"stale", 1, StatusWarning,
			"is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale; "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			expectStaleBlockedCount(mock, tc.stale)
			mock.ExpectQuery(regexp.QuoteMeta("d.issue_id FROM dependencies d")).WillReturnError(readErr)

			check := checkBlockedConsistencyDB(context.Background(), db)
			if check.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q (%s)", check.Status, tc.wantStatus, check.Message)
			}
			if strings.Contains(check.Message, "left as found") {
				t.Errorf("Message reports a count it could not read: %q", check.Message)
			}
			want := tc.wantDetail + undecidedBlockedReadFailure(errors.New("count undecided is_blocked rows: "+readErr.Error()))
			if check.Detail != want {
				t.Errorf("Detail = %q\nwant     %q", check.Detail, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}
