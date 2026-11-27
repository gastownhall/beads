package doctor

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestBlockedConsistencyCheck renders the Blocked State line from its two
// counts: with no held row the line is byte-identical to the one printed
// before rows could be held, a held count is named beside an OK verdict and
// rides in a warning's Detail, and a held count that could not be read is
// said so in Detail and never read as none.
func TestBlockedConsistencyCheck(t *testing.T) {
	const (
		okMessage    = "is_blocked flags consistent with dependency graph"
		staleMessage = "2 issue/wisp row(s) have a stale is_blocked flag — 'bd ready' may hide ready work or show blocked work"
		staleDetail  = "is_blocked is derived from the dependency graph; a skipped post-pull recompute can leave it stale"
		staleFix     = "Run: bd doctor --fix (or 'bd recompute-blocked', which also works in embedded mode)"
	)
	readErr := errors.New("read timed out")
	for _, tc := range []struct {
		name        string
		stale, held int64
		heldErr     error
		want        DoctorCheck
	}{
		{"consistent, none held", 0, 0, nil, DoctorCheck{
			Name: BlockedConsistencyCheckName, Status: StatusOK, Message: okMessage}},
		{"consistent, three held", 0, 3, nil, DoctorCheck{
			Name: BlockedConsistencyCheckName, Status: StatusOK,
			Message: okMessage + " (3 row(s) depend on a wisp this database does not have; is_blocked left as found)",
			Detail:  heldBlockedDetail}},
		{"consistent, held count failed", 0, 0, readErr, DoctorCheck{
			Name: BlockedConsistencyCheckName, Status: StatusOK, Message: okMessage,
			Detail: "could not count the rows that depend on a wisp this database does not have: read timed out"}},
		{"stale, none held", 2, 0, nil, DoctorCheck{
			Name: BlockedConsistencyCheckName, Status: StatusWarning, Message: staleMessage,
			Detail: staleDetail, Fix: staleFix}},
		{"stale, one held", 2, 1, nil, DoctorCheck{
			Name: BlockedConsistencyCheckName, Status: StatusWarning, Message: staleMessage,
			Detail: staleDetail + "; 1 row(s) depend on a wisp this database does not have; is_blocked left as found. " + heldBlockedDetail,
			Fix:    staleFix}},
		{"stale, held count failed", 2, 0, readErr, DoctorCheck{
			Name: BlockedConsistencyCheckName, Status: StatusWarning, Message: staleMessage,
			Detail: staleDetail + "; could not count the rows that depend on a wisp this database does not have: read timed out",
			Fix:    staleFix}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := blockedConsistencyCheck(tc.stale, tc.held, tc.heldErr)
			if got.Name != tc.want.Name || got.Status != tc.want.Status || got.Message != tc.want.Message ||
				got.Detail != tc.want.Detail || got.Fix != tc.want.Fix {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

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

// TestCheckBlockedConsistencyDB_ReportsAFailedHeldCount: a count of the rows
// left as found that could not be read is not a count of none. The check keeps
// its verdict, which rests on the stale count, and says in Detail that the
// other count is missing, on both branches.
func TestCheckBlockedConsistencyDB_ReportsAFailedHeldCount(t *testing.T) {
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
			want := tc.wantDetail + heldBlockedReadFailure(errors.New("count held is_blocked rows: "+readErr.Error()))
			if check.Detail != want {
				t.Errorf("Detail = %q\nwant     %q", check.Detail, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}
