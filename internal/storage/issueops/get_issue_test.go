package issueops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// missingTableError builds the driver error a Dolt query returns when its FROM
// clause names a table that does not exist. Both the embedded driver
// (dolthub/driver/v2 translateError) and the sql-server wire protocol surface
// it as a *mysql.MySQLError carrying 1146 and go-mysql-server's
// "table not found: %s" text.
func missingTableError(table string) error {
	return &mysql.MySQLError{Number: 1146, Message: "table not found: " + table}
}

// missingTableErrorMySQLText is the same condition worded the way stock MySQL
// (and Dolt's MySQL-compatible error mode) words it.
func missingTableErrorMySQLText(schema, table string) error {
	return &mysql.MySQLError{Number: 1146, Message: "Table '" + schema + "." + table + "' doesn't exist"}
}

func expectHydrationQuery(mock sqlmock.Sqlmock, table, id string, err error) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM " + table + " " + sqlbuild.LeaseJoin(table) + " WHERE id = ?")).
		WithArgs(id).
		WillReturnError(err)
}

// TestGetIssueInTxMissingLeasesTableIsAnError pins the narrowed tolerance. The
// hydration FROM clause carries sqlbuild.LeaseJoin, so a database missing the
// leases table fails the query for a row that is demonstrably present. The
// table-not-exist tolerance exists for the optional wisps plane only; folding
// a missing leases table into "row absent" hands the caller a 404 it cannot
// tell apart from a deletion.
func TestGetIssueInTxMissingLeasesTableIsAnError(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "dolt wording", err: missingTableError("leases")},
		{name: "mysql wording", err: missingTableErrorMySQLText("beads", "leases")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)

			// Both planes are primed: before the fix the issues query is
			// swallowed and the wisps query runs too, so ExpectationsWereMet
			// is deliberately not asserted here — the returned error is the
			// assertion.
			expectHydrationQuery(mock, "issues", "bd-1", tc.err)
			expectHydrationQuery(mock, "wisps", "bd-1", tc.err)

			_, err := GetIssueInTx(context.Background(), tx, "bd-1")
			if err == nil {
				t.Fatal("GetIssueInTx succeeded with no leases table")
			}
			if errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("GetIssueInTx reported the row absent for a broken lease join: %v", err)
			}
			if !strings.Contains(err.Error(), "leases") {
				t.Fatalf("error does not name the missing table: %v", err)
			}
		})
	}
}

// TestGetIssueInTxMissingWispsTableIsNotFound is the control: the tolerance
// the narrowed guard must keep. A pre-migration database has no wisps table,
// and an ID on neither plane is genuinely absent.
func TestGetIssueInTxMissingWispsTableIsNotFound(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	expectHydrationQuery(mock, "issues", "bd-1", sql.ErrNoRows)
	expectHydrationQuery(mock, "wisps", "bd-1", missingTableError("wisps"))

	_, err := GetIssueInTx(context.Background(), tx, "bd-1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetIssueInTx = %v, want ErrNotFound for a pre-migration database", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestGetIssueInTxAbsentRowIsNotFound is the second control: an ID on neither
// plane of a healthy database still answers not-found.
func TestGetIssueInTxAbsentRowIsNotFound(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	expectHydrationQuery(mock, "issues", "bd-1", sql.ErrNoRows)
	expectHydrationQuery(mock, "wisps", "bd-1", sql.ErrNoRows)

	_, err := GetIssueInTx(context.Background(), tx, "bd-1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetIssueInTx = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestUpdateIssueInTxMissingLeasesTableIsAnError covers the mutation path.
// updateIssueInTx reads the pre-update row through the same hydration query,
// so the same swallowed error told a writer its live issue did not exist.
func TestUpdateIssueInTxMissingLeasesTableIsAnError(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs("bd-1").
		WillReturnError(sql.ErrNoRows)
	expectHydrationQuery(mock, "issues", "bd-1", missingTableError("leases"))
	expectHydrationQuery(mock, "wisps", "bd-1", missingTableError("leases"))

	_, err := UpdateIssueInTx(context.Background(), tx, "bd-1", map[string]interface{}{"title": "new"}, "tester")
	if err == nil {
		t.Fatal("UpdateIssueInTx succeeded with no leases table")
	}
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("UpdateIssueInTx reported the row absent for a broken lease join: %v", err)
	}
	if !strings.Contains(err.Error(), "leases") {
		t.Fatalf("error does not name the missing table: %v", err)
	}
}
