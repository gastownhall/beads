package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const compactLogQuery = "SELECT commit_hash, committer, email, date, message, commit_order FROM dolt_log ORDER BY date DESC"

// compactRow is one dolt_log row of a compaction fixture. wantDate and
// wantAuthor are the literal --date and --author arguments the replay must
// pass for it.
type compactRow struct {
	hash, committer, email string
	date                   time.Time
	message                string
	order                  int64
	wantDate, wantAuthor   string
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
			date: s.Add(-time.Hour + 789*time.Millisecond), message: "bd: old work", order: 2,
			wantDate: "2026-03-04T04:06:07.789Z"},
		kept: []compactRow{
			{hash: "k1k1k1k1k1k1", committer: "Original Author", email: "orig@example.com",
				date: s.Add(123 * time.Millisecond), message: "bd: edit A", order: 3,
				wantDate: "2026-03-04T05:06:07.123Z", wantAuthor: "Original Author <orig@example.com>"},
			// Empty commit in the same second, 333 ms later.
			{hash: "k2k2k2k2k2k2", committer: "Original Author", email: "orig@example.com",
				date: s.Add(456 * time.Millisecond), message: "bd: empty", order: 4,
				wantDate: "2026-03-04T05:06:07.456Z", wantAuthor: "Original Author <orig@example.com>"},
			// Scanned in a non-UTC zone; the message starts with a dash.
			{hash: "k3k3k3k3k3k3", committer: "root", email: "root@%",
				date: time.Date(2026, 3, 4, 1, 10, 0, 789e6, time.FixedZone("EST", -5*3600)), message: "-m looks like a flag", order: 5,
				wantDate: "2026-03-04T06:10:00.789Z", wantAuthor: "root <root@%>"},
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

// rebuilt returns the dolt_log rows of compact-tmp after a faithful replay:
// the initial commit, the base at the boundary's date, then the kept commits
// under new hashes with their original metadata.
func (f compactFixture) rebuilt() []compactRow {
	rows := []compactRow{f.initial, {hash: "basebasebase", committer: "root", email: "root@%",
		date: f.boundary.date, message: "compact: squash 2 commits into base snapshot", order: 2}}
	for i, r := range f.kept {
		r.hash = strings.Repeat(string(rune('p'+i)), 12)
		r.order = int64(3 + i)
		rows = append(rows, r)
	}
	return rows
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
	expectExec(mock, "CALL DOLT_COMMIT('-Am', ?, '--date', ?)", "compact: squash 2 commits into base snapshot", f.boundary.wantDate)
	for _, r := range f.kept {
		expectExec(mock, "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", r.hash)
		expectExec(mock, "CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', ?, '--author', ?)", r.wantDate, r.wantAuthor)
	}
	expectCompactLog(mock, f.rebuilt()...)
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

// TestCompactRestoresCommitAuthor pins that the amend carries the original
// committer and email as --author instead of leaving the SQL session's
// identity, and passes no -m: the message cherry-pick copied is kept as is,
// so one starting with a dash never reaches Dolt's argument parser.
func TestCompactRestoresCommitAuthor(t *testing.T) {
	db, mock := newCompactMock(t)
	f := newCompactFixture()
	f.kept = f.kept[:1]
	f.kept[0].message = "-m bd: edit A"
	expectCompactSuccess(mock, f)

	if err := Compact(context.Background(), db, f.initial.hash, f.boundary.hash, 2, f.recentHashes()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactDatesSquashedBase pins that the squashed base, which holds the
// boundary commit's tree, carries the boundary commit's date, so AS OF any
// time from that date up to the first kept commit still resolves to it.
func TestCompactDatesSquashedBase(t *testing.T) {
	db, mock := newCompactMock(t)
	f := newCompactFixture()
	f.boundary.date = time.Date(2026, 3, 1, 12, 0, 0, 5e6, time.FixedZone("CET", 3600))
	f.boundary.wantDate = "2026-03-01T11:00:00.005Z"
	expectCompactSuccess(mock, f)

	if err := Compact(context.Background(), db, f.initial.hash, f.boundary.hash, 2, f.recentHashes()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactRefusesUnknownRecentCommit pins the refusals: a commit missing
// from the log or an identity --author cannot carry stops Compact before the
// temp branch exists, and a replay whose read-back differs from the original
// stops it before main moves (no DOLT_RESET('--hard')), leaving no temp branch.
func TestCompactRefusesUnknownRecentCommit(t *testing.T) {
	before := []struct {
		name   string
		mutate func(*compactFixture) (logRows []compactRow, wantHash string)
	}{
		{"recent hash not in log", func(f *compactFixture) ([]compactRow, string) {
			return []compactRow{f.initial, f.boundary, f.kept[0], f.kept[2]}, f.kept[1].hash
		}},
		{"boundary hash not in log", func(f *compactFixture) ([]compactRow, string) {
			return append([]compactRow{f.initial}, f.kept...), f.boundary.hash
		}},
		{"name with closing paren", func(f *compactFixture) ([]compactRow, string) {
			f.kept[1].committer = "Ann (bot) Smith"
			return append([]compactRow{f.initial, f.boundary}, f.kept...), f.kept[1].hash
		}},
		{"empty name", func(f *compactFixture) ([]compactRow, string) {
			f.kept[0].committer = ""
			return append([]compactRow{f.initial, f.boundary}, f.kept...), f.kept[0].hash
		}},
		{"email with angle bracket", func(f *compactFixture) ([]compactRow, string) {
			f.kept[2].email = "a<b@example.com"
			return append([]compactRow{f.initial, f.boundary}, f.kept...), f.kept[2].hash
		}},
	}
	for _, tc := range before {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newCompactMock(t)
			f := newCompactFixture()
			rows, wantHash := tc.mutate(&f)
			expectCompactLog(mock, rows...)

			err := Compact(context.Background(), db, f.initial.hash, f.boundary.hash, 2, f.recentHashes())
			if err == nil || !strings.Contains(err.Error(), wantHash) {
				t.Fatalf("Compact error = %v, want a refusal naming %s", err, wantHash)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("read-back date differs", func(t *testing.T) {
		db, mock := newCompactMock(t)
		f := newCompactFixture()
		expectCompactLog(mock, append([]compactRow{f.initial, f.boundary}, f.kept...)...)
		expectExec(mock, "CALL DOLT_BRANCH('compact-tmp', ?)", f.boundary.hash)
		expectExec(mock, "CALL DOLT_CHECKOUT('compact-tmp')")
		expectExec(mock, "CALL DOLT_RESET('--soft', ?)", f.initial.hash)
		expectExec(mock, "CALL DOLT_COMMIT('-Am', ?, '--date', ?)", "compact: squash 2 commits into base snapshot", f.boundary.wantDate)
		for _, r := range f.kept {
			expectExec(mock, "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", r.hash)
			expectExec(mock, "CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', ?, '--author', ?)", r.wantDate, r.wantAuthor)
		}
		rebuilt := f.rebuilt()
		rebuilt[2].date = rebuilt[2].date.Add(time.Millisecond)
		expectCompactLog(mock, rebuilt...)
		// Deferred cleanup only: main is never reset.
		expectExec(mock, "CALL DOLT_CHECKOUT('main')")
		expectExec(mock, "CALL DOLT_BRANCH('-D', 'compact-tmp')")

		err := Compact(context.Background(), db, f.initial.hash, f.boundary.hash, 2, f.recentHashes())
		if err == nil || !strings.Contains(err.Error(), f.kept[0].hash[:8]) {
			t.Fatalf("Compact error = %v, want a refusal naming %s", err, f.kept[0].hash[:8])
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty email is carried", func(t *testing.T) {
		db, mock := newCompactMock(t)
		f := newCompactFixture()
		f.kept[1].email = ""
		f.kept[1].wantAuthor = "Original Author <>"
		expectCompactSuccess(mock, f)

		if err := Compact(context.Background(), db, f.initial.hash, f.boundary.hash, 2, f.recentHashes()); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
