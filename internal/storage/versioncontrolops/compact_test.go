package versioncontrolops

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// The replayed commits must keep their original dates: a tracker's last_sync
// stored before the compaction is resolved AS OF a commit date, and a history
// re-dated to the compaction time leaves only the initial commit before it.
func TestCompactRestoresCommitMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT commit_hash, committer, email, DATE_FORMAT(date, '%Y-%m-%dT%H:%i:%s.%fZ'), message FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "message"}).
			AddRow("r2hash", "Ann", "ann@example.com", "2026-10-02T09:00:00.250000Z", "bd: update x-2").
			AddRow("r1hash", "Bob", "bob@example.com", "2026-10-01T08:00:00.125000Z", "bd: create x-1").
			AddRow("bndhash", "Ann", "ann@example.com", "2026-09-01T07:00:00.000000Z", "bd: close x-0").
			AddRow("inithash", "Ann", "ann@example.com", "2026-01-01T00:00:00.000000Z", "Initialize data repository"))

	exec := func(query string, args ...any) {
		expected := make([]driver.Value, len(args))
		for i, a := range args {
			expected[i] = a
		}
		mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(expected...).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	exec("CALL DOLT_BRANCH('compact-tmp', ?)", "bndhash")
	exec("CALL DOLT_CHECKOUT('compact-tmp')")
	exec("CALL DOLT_RESET('--soft', ?)", "inithash")
	exec("CALL DOLT_COMMIT('-Am', ?, '--date', ?)", "compact: squash 2 commits into base snapshot", "2026-09-01T07:00:00.000000Z")
	exec("CALL DOLT_CHERRY_PICK('--allow-empty', ?)", "r1hash")
	exec("CALL DOLT_COMMIT('--amend', '--allow-empty', '-m', ?, '--date', ?, '--author', ?)",
		"bd: create x-1", "2026-10-01T08:00:00.125000Z", "Bob <bob@example.com>")
	exec("CALL DOLT_CHERRY_PICK('--allow-empty', ?)", "r2hash")
	exec("CALL DOLT_COMMIT('--amend', '--allow-empty', '-m', ?, '--date', ?, '--author', ?)",
		"bd: update x-2", "2026-10-02T09:00:00.250000Z", "Ann <ann@example.com>")
	exec("CALL DOLT_CHECKOUT('main')")
	exec("CALL DOLT_RESET('--hard', 'compact-tmp')")
	exec("CALL DOLT_BRANCH('-D', 'compact-tmp')")

	if err := Compact(context.Background(), db, "inithash", "bndhash", 2, []string{"r1hash", "r2hash"}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A hash missing from the log must stop the compaction before the temp branch
// exists, rather than replaying a commit it cannot re-date.
func TestCompactRefusesUnknownRecentCommit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT commit_hash").
		WillReturnRows(sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "message"}).
			AddRow("bndhash", "Ann", "ann@example.com", "2026-09-01T07:00:00.000000Z", "bd: close x-0"))

	err = Compact(context.Background(), db, "inithash", "bndhash", 1, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Compact error = %v, want one naming the missing commit", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
