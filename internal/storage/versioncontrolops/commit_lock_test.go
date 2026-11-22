package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// commitLockRecorder wraps a DBConn and records every statement issued on it, in
// order, so a test can assert on the sequence StageAndCommit sends to one
// session regardless of which DBConn method carries each statement.
type commitLockRecorder struct {
	inner DBConn
	stmts []string
}

func (r *commitLockRecorder) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	r.stmts = append(r.stmts, q)
	return r.inner.ExecContext(ctx, q, args...)
}

func (r *commitLockRecorder) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	r.stmts = append(r.stmts, q)
	return r.inner.QueryContext(ctx, q, args...)
}

func (r *commitLockRecorder) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	r.stmts = append(r.stmts, q)
	return r.inner.QueryRowContext(ctx, q, args...)
}

// TestStageAndCommitSerializesAddAndCommitUnderNamedLock reproduces GH#8590
// (bd-72i8dp): with several bd clients on one Dolt sql-server, two
// StageAndCommit calls whose DOLT_ADD/DOLT_COMMIT pairs interleave make Dolt
// merge the moved HEAD into the second commit's staged root and record
// conflict artifacts in that commit. The fix holds a server-wide named lock
// (GET_LOCK) on the same session across the per-table DOLT_ADD calls and the
// DOLT_COMMIT, and releases it (RELEASE_LOCK) afterwards.
//
// The test is deterministic: it records the statement sequence StageAndCommit
// issues on its connection and requires GET_LOCK before the first DOLT_ADD and
// RELEASE_LOCK after DOLT_COMMIT. At the base commit no lock is taken at all,
// so nothing serializes the add+commit pair across clients.
func TestStageAndCommitSerializesAddAndCommitUnderNamedLock(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	// Where the lock sits relative to the dolt_status checks is the fix's
	// choice; the order this test cares about is asserted on the recording.
	mock.MatchExpectationsInOrder(false)

	mock.ExpectQuery(`GET_LOCK`).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectQuery(matchPendingQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).
		WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(matchStagedQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`CALL DOLT_COMMIT`).
		WithArgs("real change", "tester").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	mock.ExpectQuery(`RELEASE_LOCK`).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	conn := &commitLockRecorder{inner: db}
	// The base code fails on the unexpected-call path only if it issued
	// something sqlmock did not expect; the defect is what it does NOT issue,
	// which the recording below catches.
	_ = StageAndCommit(context.Background(), conn, map[string]bool{"issues": true}, "real change", "tester")

	idx := func(substr string) int {
		for i, s := range conn.stmts {
			if strings.Contains(s, substr) {
				return i
			}
		}
		return -1
	}
	lastIdx := func(substr string) int {
		last := -1
		for i, s := range conn.stmts {
			if strings.Contains(s, substr) {
				last = i
			}
		}
		return last
	}

	getLock, firstAdd := idx("GET_LOCK"), idx("DOLT_ADD")
	commit, release := lastIdx("DOLT_COMMIT"), lastIdx("RELEASE_LOCK")
	if firstAdd < 0 || commit < 0 {
		t.Fatalf("StageAndCommit did not stage and commit a pending change; statements: %q", conn.stmts)
	}
	if getLock < 0 || getLock > firstAdd {
		t.Errorf("DOLT_ADD issued without first taking a GET_LOCK on the same session (GH#8590): concurrent writers' add+commit pairs can interleave and commit Dolt conflict artifacts; statements: %q", conn.stmts)
	}
	if release < 0 || release < commit {
		t.Errorf("named commit lock not released with RELEASE_LOCK after DOLT_COMMIT; statements: %q", conn.stmts)
	}
}

// TestCommitLockNamePinsTheCrossVersionContract pins CommitLockName's shape:
// it is a cross-version contract (a mixed-version fleet of bd binaries
// serializes against the same string, and a name change would silently undo
// every plane's serialization), so the readable form, the case folding, the
// revision-suffix drop, and the >64-byte hash must never drift.
func TestCommitLockNamePinsTheCrossVersionContract(t *testing.T) {
	cases := []struct {
		database string
		want     string
	}{
		{"beads", "bd_commit:beads"},
		// Dolt database names are case-insensitive; the dolt and uow planes
		// must derive the identical string whatever casing their configs use.
		{"BeadsForge", "bd_commit:beadsforge"},
		// A revision suffix (branch or head) is the same database's working
		// set: one lock, not one per branch.
		{"beads/main", "bd_commit:beads"},
		{"Beads/feature/HEAD", "bd_commit:beads"},
		{"", "bd_commit:"},
	}
	for _, tc := range cases {
		if got := CommitLockName(tc.database); got != tc.want {
			t.Errorf("CommitLockName(%q) = %q, want %q", tc.database, got, tc.want)
		}
	}
	// A name past the 64-byte GET_LOCK limit folds to the documented
	// "bd_commit:" + fnv64a hex, and cannot collide with the readable form of
	// a short database.
	raw := "db" + strings.Repeat("x", 100)
	got := CommitLockName(raw)
	if len(got) > commitLockNameMaxLength {
		t.Fatalf("CommitLockName of a 102-char database is %d bytes, want <= %d", len(got), commitLockNameMaxLength)
	}
	if got != CommitLockName("db"+strings.Repeat("x", 100)) {
		t.Fatal("CommitLockName is not deterministic")
	}
	if got == CommitLockName(strings.Repeat("y", 102)) {
		t.Fatal("the hashed form lost the database discrimination it exists to keep")
	}
}

// TestStageAndCommitWithoutCommitLockIssuesNoLockStatements is the
// embedded-mode contract (AC-8's unit level): a caller that threads
// WithoutCommitLock — the embedded store's planes — gets the full staged
// commit with NO GET_LOCK/RELEASE_LOCK statements, since a single-process
// store has no concurrent sessions to exclude.
func TestStageAndCommitWithoutCommitLockIssuesNoLockStatements(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(matchPendingQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).
		WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(matchStagedQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`CALL DOLT_COMMIT`).
		WithArgs("embedded change", "tester").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))

	conn := &commitLockRecorder{inner: db}
	if err := StageAndCommit(WithoutCommitLock(context.Background()), conn,
		map[string]bool{"issues": true}, "embedded change", "tester"); err != nil {
		t.Fatalf("StageAndCommit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the staged commit did not run in full: %v", err)
	}
	for _, s := range conn.stmts {
		if strings.Contains(s, "GET_LOCK") || strings.Contains(s, "RELEASE_LOCK") {
			t.Errorf("embedded (WithoutCommitLock) commit issued %q: embedded mode takes no lock statements", s)
		}
	}
}

// TestStageAndCommitReleasesLockOnFailure is AC-4's unit level: the failure
// return paths of the locked section — a failed DOLT_ADD, a failed
// DOLT_COMMIT, and a nothing-staged skip — must all release the named lock
// with RELEASE_LOCK before returning.
func TestStageAndCommitReleasesLockOnFailure(t *testing.T) {
	t.Run("FailedDoltAdd", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		addErr := errors.New("dolt add rejected")
		mock.ExpectQuery(matchPendingQuery.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`GET_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
		mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).
			WithArgs("issues").
			WillReturnError(addErr)
		mock.ExpectQuery(`RELEASE_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

		conn := &commitLockRecorder{inner: db}
		if err := StageAndCommit(context.Background(), conn, map[string]bool{"issues": true}, "x", "a"); err == nil {
			t.Fatal("StageAndCommit should surface the DOLT_ADD failure")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("lock not released on the failed-DOLT_ADD path: %v", err)
		}
		last := conn.stmts[len(conn.stmts)-1]
		if !strings.Contains(last, "RELEASE_LOCK") {
			t.Errorf("last statement was %q, want the lock release", last)
		}
	})

	t.Run("FailedDoltCommit", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(matchPendingQuery.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`GET_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
		mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).
			WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(matchStagedQuery.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`CALL DOLT_COMMIT`).
			WillReturnError(errors.New("dolt commit refused"))
		mock.ExpectQuery(`RELEASE_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

		conn := &commitLockRecorder{inner: db}
		if err := StageAndCommit(context.Background(), conn, map[string]bool{"issues": true}, "x", "a"); err == nil {
			t.Fatal("StageAndCommit should surface the DOLT_COMMIT failure")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("lock not released on the failed-DOLT_COMMIT path: %v", err)
		}
	})

	t.Run("NothingStaged", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(matchPendingQuery.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`GET_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
		mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).
			WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(matchStagedQuery.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`RELEASE_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

		conn := &commitLockRecorder{inner: db}
		if err := StageAndCommit(context.Background(), conn, map[string]bool{"issues": true}, "x", "a"); err != nil {
			t.Fatalf("nothing-staged skip should be a clean nil, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("lock not released on the nothing-staged path: %v", err)
		}
	})

	t.Run("TimedOutLockNeverReleased", func(t *testing.T) {
		// A lock that was never granted must never be released: releasing
		// another session's lock on the same name is the only way this plane
		// could make things worse than not locking.
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(matchPendingQuery.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`GET_LOCK`).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(0))
		mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).
			WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(matchStagedQuery.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(`CALL DOLT_COMMIT`).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}))

		conn := &commitLockRecorder{inner: db}
		if err := StageAndCommit(context.Background(), conn, map[string]bool{"issues": true}, "x", "a"); err != nil {
			t.Fatalf("fail-open commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unexpected statements on the fail-open path: %v", err)
		}
		for _, s := range conn.stmts {
			if strings.Contains(s, "RELEASE_LOCK") {
				t.Errorf("issued %q without holding the lock: a not-granted lock must never be released", s)
			}
		}
	})
}

// TestCommitLockWaitSecondsPinsTheDeadlineCap pins how the GET_LOCK wait is
// derived: the configured wait by default, and never closer than one second to
// the caller's own deadline, so the driver does not kill the connection
// mid-wait.
func TestCommitLockWaitSecondsPinsTheDeadlineCap(t *testing.T) {
	if got := commitLockWaitSeconds(context.Background(), 0); got != 5 {
		t.Errorf("default wait = %ds, want 5s", got)
	}
	if got := commitLockWaitSeconds(context.Background(), 3*time.Second); got != 3 {
		t.Errorf("configured 3s wait = %ds, want 3s", got)
	}
	// A 3s deadline leaves ~2s after the 1s reservation; a 500ms deadline has
	// nothing left and degrades to one try.
	ctxShort, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	short := commitLockWaitSeconds(ctxShort, 10*time.Second)
	limit := commitLockWaitSeconds(context.Background(), 500*time.Millisecond)
	if short < 1 || short > 2 {
		t.Errorf("capped wait = %ds, want 1-2s for a 3s deadline", short)
	}
	if limit != 0 {
		t.Errorf("sub-reservation deadline wait = %ds, want a single try (0)", limit)
	}
}
