package issueops

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestChangedIssueIDsInTxRunsOnAConnection pins the connection-level form of
// DoltStore.ChangedIssueIDs that proxied-server auto-export runs (GH#8490):
// the four-table dolt_diff union over the given refs on whatever DBTX it is
// handed — here a pinned *sql.Conn — split into Upserted and Removed.
func TestChangedIssueIDsInTxRunsOnAConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer conn.Close()

	mock.ExpectQuery(regexp.QuoteMeta("FROM dolt_diff('abc123', 'WORKING', 'issues')") + `(?s).*` +
		regexp.QuoteMeta("FROM dolt_diff('abc123', 'WORKING', 'labels')") + `(?s).*` +
		regexp.QuoteMeta("FROM dolt_diff('abc123', 'WORKING', 'dependencies')") + `(?s).*` +
		regexp.QuoteMeta("FROM dolt_diff('abc123', 'WORKING', 'comments')")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "is_removed"}).
			AddRow("bd-1", 0).
			AddRow("bd-2", 1).
			AddRow("bd-3", 0))

	got, err := ChangedIssueIDsInTx(ctx, conn, "abc123", "WORKING")
	if err != nil {
		t.Fatalf("ChangedIssueIDsInTx: %v", err)
	}
	if !slices.Equal(got.Upserted, []string{"bd-1", "bd-3"}) || !slices.Equal(got.Removed, []string{"bd-2"}) {
		t.Fatalf("ChangedIssueIDsInTx = %+v, want Upserted [bd-1 bd-3], Removed [bd-2]", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestChangedIssueIDsInTxRefusesUnsafeRefs pins that a ref which could break
// out of the inlined dolt_diff() literal is refused before any SQL runs.
func TestChangedIssueIDsInTxRefusesUnsafeRefs(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{from: "", to: "WORKING", want: "required"},
		{from: "abc123", to: "", want: "required"},
		{from: "abc'); DROP TABLE issues; --", to: "WORKING", want: "fromCommit"},
		{from: "abc123", to: "main~1", want: "toCommit"},
		{from: strings.Repeat("a", 65), to: "WORKING", want: "fromCommit"},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		_, err = ChangedIssueIDsInTx(context.Background(), db, tc.from, tc.to)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ChangedIssueIDsInTx(%q, %q) err = %v, want one naming %q", tc.from, tc.to, err, tc.want)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("ChangedIssueIDsInTx(%q, %q) ran SQL: %v", tc.from, tc.to, err)
		}
		_ = db.Close()
	}
}
