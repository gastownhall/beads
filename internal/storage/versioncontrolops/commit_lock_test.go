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
