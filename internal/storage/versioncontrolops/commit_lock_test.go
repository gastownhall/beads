package versioncontrolops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// GH#8590 (bd-72i8dp): StageAndCommitSerialized holds the per-database commit
// lock from just before the first DOLT_ADD until DOLT_COMMIT returns, and
// releases it on every return path. StageAndCommit, the embedded and admin
// entry point, takes no lock.

const commitLockTestDB = "beads"

func expectCommitLockGrant(mock sqlmock.Sqlmock, granted int) {
	mock.ExpectQuery(`SELECT GET_LOCK\(\?, \?\)`).
		WithArgs(CommitLockName(commitLockTestDB), int64(DefaultCommitLockWait.Seconds())).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(granted))
}

func expectCommitLockRelease(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT RELEASE_LOCK\(\?\)`).
		WithArgs(CommitLockName(commitLockTestDB)).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
}

// TestCommitLockStageAndCommitSerializedReleasesOnEveryPath covers AC-4 at
// the unit level: the lock is taken after the pending check and released
// exactly once, after the last statement, on success, a failed DOLT_ADD, a
// failed DOLT_COMMIT and nothing staged; a timed-out acquire releases nothing.
func TestCommitLockStageAndCommitSerializedReleasesOnEveryPath(t *testing.T) {
	addErr := errors.New("table not found: no_such_table")
	commitErr := errors.New("invalid author")
	cases := []struct {
		name    string
		granted int
		addErr  error
		staged  int
		commit  error
		wantErr error
	}{
		{name: "success", granted: 1, staged: 1},
		{name: "failed DOLT_ADD", granted: 1, addErr: addErr, wantErr: addErr},
		{name: "failed DOLT_COMMIT", granted: 1, staged: 1, commit: commitErr, wantErr: commitErr},
		{name: "nothing staged", granted: 1, staged: 0},
		{name: "timed out acquire commits unlocked", granted: 0, staged: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			ctx := context.Background()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("pin conn: %v", err)
			}
			defer conn.Close()

			mock.ExpectQuery(matchPendingQuery.String()).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			expectCommitLockGrant(mock, tc.granted)
			add := mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).WithArgs("issues")
			if tc.addErr != nil {
				add.WillReturnError(tc.addErr)
			} else {
				add.WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(matchStagedQuery.String()).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.staged))
				if tc.staged > 0 {
					commit := mock.ExpectQuery(`CALL DOLT_COMMIT`).WithArgs("msg", "tester")
					if tc.commit != nil {
						commit.WillReturnError(tc.commit)
					} else {
						commit.WillReturnRows(sqlmock.NewRows([]string{"hash"}))
					}
				}
			}
			if tc.granted == 1 {
				expectCommitLockRelease(mock)
			}

			var acquired []*CommitLock
			cfg := CommitLockConfig{Database: commitLockTestDB, OnAcquire: func(_ context.Context, l *CommitLock) {
				acquired = append(acquired, l)
			}}
			err = StageAndCommitSerialized(ctx, conn, cfg, map[string]bool{"issues": true}, "msg", "tester")
			if tc.wantErr == nil && err != nil {
				t.Fatalf("StageAndCommitSerialized: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("StageAndCommitSerialized err = %v, want %v", err, tc.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("commit-lock statement sequence: %v", err)
			}
			if len(acquired) != 1 {
				t.Fatalf("OnAcquire ran %d times, want 1", len(acquired))
			}
			if acquired[0].Held() {
				t.Fatalf("lock still marked held after the call returned")
			}
			if acquired[0].TimedOut != (tc.granted == 0) {
				t.Fatalf("TimedOut = %v, want %v", acquired[0].TimedOut, tc.granted == 0)
			}
		})
	}
}

// TestCommitLockSkippedWhenNothingPendingUnit covers AC-5 at the unit level:
// with nothing pending, StageAndCommitSerialized issues no GET_LOCK.
func TestCommitLockSkippedWhenNothingPendingUnit(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin conn: %v", err)
	}
	defer conn.Close()

	mock.ExpectQuery(matchPendingQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// No GET_LOCK expectation: sqlmock fails an unexpected one.
	cfg := CommitLockConfig{Database: commitLockTestDB}
	if err := StageAndCommitSerialized(ctx, conn, cfg, map[string]bool{"metadata": true}, "noop", "tester"); err != nil {
		t.Fatalf("StageAndCommitSerialized: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("statement sequence: %v", err)
	}
}

// TestCommitLockNotTakenByStageAndCommit covers AC-8 at the unit level:
// StageAndCommit, the entry point the embedded store's RunInTransaction and
// version-control commits use, stages and commits without GET_LOCK or
// RELEASE_LOCK.
func TestCommitLockNotTakenByStageAndCommit(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(matchPendingQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(matchStagedQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`CALL DOLT_COMMIT`).WithArgs("embedded", "tester").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))

	if err := StageAndCommit(context.Background(), db, map[string]bool{"issues": true}, "embedded", "tester"); err != nil {
		t.Fatalf("StageAndCommit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("statement sequence: %v", err)
	}
}

// TestCommitLockName pins the lock key, a cross-version contract: bd
// processes of different versions on one server serialize only if they
// compute the same string, so these goldens must never change silently.
func TestCommitLockName(t *testing.T) {
	long := strings.Repeat("a", 64)
	cases := []struct {
		database string
		want     string
	}{
		{"beads", "bd_commit:beads"},
		{"Beads_Prod", "bd_commit:beads_prod"},
		{"beads/feature-x", "bd_commit:beads"},
		{"BEADS/Main", "bd_commit:beads"},
		{long, "bd_commit:db007849f35ebbe5"},
		{"Beads_" + strings.Repeat("x", 60) + "/branch", "bd_commit:5acf4ad27539d0a7"},
	}
	for _, tc := range cases {
		got := CommitLockName(tc.database)
		if got != tc.want {
			t.Errorf("CommitLockName(%q) = %q, want %q", tc.database, got, tc.want)
		}
		if len(got) > commitLockNameMaxLength {
			t.Errorf("CommitLockName(%q) is %d bytes, over the %d-byte GET_LOCK limit", tc.database, len(got), commitLockNameMaxLength)
		}
		if again := CommitLockName(tc.database); again != got {
			t.Errorf("CommitLockName(%q) not deterministic: %q then %q", tc.database, got, again)
		}
	}
	if CommitLockName("beads") == CommitLockName("other_db") {
		t.Errorf("distinct databases share a commit lock name")
	}
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
	// A 3s deadline leaves ~2s after the 1s reservation. A configured
	// sub-second wait with no deadline truncates to whole seconds: one try (0).
	ctxShort, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	short := commitLockWaitSeconds(ctxShort, 10*time.Second)
	limit := commitLockWaitSeconds(context.Background(), 500*time.Millisecond)
	if short < 1 || short > 2 {
		t.Errorf("capped wait = %ds, want 1-2s for a 3s deadline", short)
	}
	if limit != 0 {
		t.Errorf("sub-second configured wait = %ds, want a single try (0)", limit)
	}
}

// TestCommitAllSerializedHoldsTheLockAroundTheCommit pins the maintenance
// commit of the proxied `bd dolt commit` and pre-backup commit: the lock is
// taken after the pending check, keyed on the session's own database when the
// config names none, and released after DOLT_COMMIT on every return path. With
// nothing pending there is no lock and the author is never resolved.
func TestCommitAllSerializedHoldsTheLockAroundTheCommit(t *testing.T) {
	commitErr := errors.New("invalid author")
	cases := []struct {
		name          string
		database      string // CommitLockConfig.Database; "" reads it from the session
		sessionDB     string // what SELECT DATABASE() returns
		pending       int
		author        bool
		commitErr     error
		wantCommitted bool
		wantErr       error
	}{
		{name: "commit with author", database: commitLockTestDB, pending: 1, author: true, wantCommitted: true},
		{name: "commit without author", database: commitLockTestDB, pending: 1, wantCommitted: true},
		{name: "session database keys the lock", sessionDB: "Beads/main", pending: 1, wantCommitted: true},
		{name: "nothing to commit after the wait", database: commitLockTestDB, pending: 1,
			commitErr: errors.New("nothing to commit"), wantCommitted: false},
		{name: "failed commit", database: commitLockTestDB, pending: 1, commitErr: commitErr, wantErr: commitErr},
		{name: "nothing pending", database: commitLockTestDB, pending: 0, author: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			ctx := context.Background()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("pin conn: %v", err)
			}
			defer conn.Close()

			mock.ExpectQuery(matchPendingQuery.String()).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.pending))
			if tc.pending > 0 {
				lockName := CommitLockName(commitLockTestDB)
				if tc.database == "" {
					mock.ExpectQuery(`SELECT DATABASE\(\)`).
						WillReturnRows(sqlmock.NewRows([]string{"db"}).AddRow(tc.sessionDB))
					lockName = CommitLockName(tc.sessionDB)
				}
				mock.ExpectQuery(`SELECT GET_LOCK\(\?, \?\)`).
					WithArgs(lockName, int64(DefaultCommitLockWait.Seconds())).
					WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
				var commit *sqlmock.ExpectedExec
				if tc.author {
					commit = mock.ExpectExec(`CALL DOLT_COMMIT\('-Am', \?, '--author', \?\)`).
						WithArgs("flush", "Ann <ann@example.com>")
				} else {
					commit = mock.ExpectExec(`CALL DOLT_COMMIT\('-Am', \?\)`).WithArgs("flush")
				}
				if tc.commitErr != nil {
					commit.WillReturnError(tc.commitErr)
				} else {
					commit.WillReturnResult(sqlmock.NewResult(0, 0))
				}
				mock.ExpectQuery(`SELECT RELEASE_LOCK\(\?\)`).WithArgs(lockName).
					WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
			}

			var author func() string
			resolved := 0
			if tc.author {
				author = func() string { resolved++; return "Ann <ann@example.com>" }
			}
			committed, err := CommitAllSerialized(ctx, conn, CommitLockConfig{Database: tc.database}, "flush", author)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("CommitAllSerialized: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("CommitAllSerialized err = %v, want %v", err, tc.wantErr)
			}
			if committed != tc.wantCommitted {
				t.Fatalf("committed = %v, want %v", committed, tc.wantCommitted)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("statement sequence: %v", err)
			}
			wantResolved := 0
			if tc.author && tc.pending > 0 {
				wantResolved = 1
			}
			if resolved != wantResolved {
				t.Fatalf("author resolved %d times, want %d", resolved, wantResolved)
			}
		})
	}
}
