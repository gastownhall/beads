package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const compactLogQuery = "SELECT commit_hash, committer, email, date, message, commit_order FROM dolt_log ORDER BY date DESC"

// compactRow is one dolt_log row of a compaction fixture. wantDate is the
// literal --date argument the replay must pass for it.
type compactRow struct {
	hash, committer, email string
	date                   time.Time
	message                string
	order                  int64
	wantDate               string
}

// compactFixture is a history of an initial commit, a boundary commit and the
// kept window, in graph order.
type compactFixture struct {
	initial, boundary compactRow
	kept              []compactRow
}

func newCompactFixture() compactFixture {
	s := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	return compactFixture{
		initial: compactRow{hash: "iiiiiiiiiiii", committer: "beads", email: "beads@local",
			date: s.Add(-48 * time.Hour), message: "Initialize data repository", order: 1},
		boundary: compactRow{hash: "bbbbbbbbbbbb", committer: "beads", email: "beads@local",
			date: s.Add(-time.Hour + 789*time.Millisecond), message: "bd: old work", order: 2},
		kept: []compactRow{
			{hash: "k1k1k1k1k1k1", committer: "Original Author", email: "orig@example.com",
				date: s.Add(123 * time.Millisecond), message: "bd: edit A", order: 3,
				wantDate: "2026-03-04T05:06:07.123Z"},
			// Empty commit in the same second, 333 ms later.
			{hash: "k2k2k2k2k2k2", committer: "Original Author", email: "orig@example.com",
				date: s.Add(456 * time.Millisecond), message: "bd: empty", order: 4,
				wantDate: "2026-03-04T05:06:07.456Z"},
			// Scanned in a non-UTC zone; the message starts with a dash.
			{hash: "k3k3k3k3k3k3", committer: "root", email: "root@%",
				date: time.Date(2026, 3, 4, 1, 10, 0, 789e6, time.FixedZone("EST", -5*3600)), message: "-m looks like a flag", order: 5,
				wantDate: "2026-03-04T06:10:00.789Z"},
		},
	}
}

func (f compactFixture) recentHashes() []string {
	hashes := make([]string, len(f.kept))
	for i, r := range f.kept {
		hashes[i] = r.hash
	}
	return hashes
}

func newCompactMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func expectCompactLog(mock sqlmock.Sqlmock, rows ...compactRow) {
	r := sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "message", "commit_order"})
	for _, row := range rows {
		r.AddRow(row.hash, row.committer, row.email, row.date, row.message, row.order)
	}
	mock.ExpectQuery(compactLogQuery).WillReturnRows(r)
}

func expectExec(mock sqlmock.Sqlmock, query string, args ...driver.Value) {
	e := mock.ExpectExec(query)
	if len(args) > 0 {
		e.WithArgs(args...)
	}
	e.WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectCompactSuccess expects the full statement sequence of a successful
// compaction of f.
func expectCompactSuccess(mock sqlmock.Sqlmock, f compactFixture) {
	expectCompactLog(mock, append([]compactRow{f.initial, f.boundary}, f.kept...)...)
	expectExec(mock, "CALL DOLT_BRANCH('compact-tmp', ?)", f.boundary.hash)
	expectExec(mock, "CALL DOLT_CHECKOUT('compact-tmp')")
	expectExec(mock, "CALL DOLT_RESET('--soft', ?)", f.initial.hash)
	expectExec(mock, "CALL DOLT_COMMIT('-Am', ?)", "compact: squash 2 commits into base snapshot")
	for _, r := range f.kept {
		expectExec(mock, "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", r.hash)
		expectExec(mock, "CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', ?)", r.wantDate)
	}
	expectExec(mock, "CALL DOLT_CHECKOUT('main')")
	expectExec(mock, "CALL DOLT_RESET('--hard', 'compact-tmp')")
	expectExec(mock, "CALL DOLT_BRANCH('-D', 'compact-tmp')")
}

// TestCompactRestoresCommitDates pins #7294: each cherry-picked commit is
// amended back to its original date, to the millisecond and in UTC, so two
// commits within one second keep their order.
func TestCompactRestoresCommitDates(t *testing.T) {
	db, mock := newCompactMock(t)
	f := newCompactFixture()
	expectCompactSuccess(mock, f)

	if err := Compact(context.Background(), db, f.initial.hash, f.boundary.hash, 2, f.recentHashes()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
