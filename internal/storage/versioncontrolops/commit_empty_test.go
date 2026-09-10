package versioncontrolops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestStageAndCommitSkipsWhenNothingPending is a regression test for the
// high-frequency "nothing to commit" Dolt warning spam.
//
// Root cause: a write statement can succeed and mark its table dirty without
// changing any rows (e.g. SetMetadata's "INSERT ... ON DUPLICATE KEY UPDATE
// value = VALUES(value)" re-writing the same value). The old StageAndCommit
// guarded only on len(dirtyTables)==0, so it still issued DOLT_ADD + DOLT_COMMIT
// for such no-op transactions; Dolt rejected each with a server-side
// "nothing to commit" warning that floods the log at heartbeat cadence.
//
// The fix consults Dolt's authoritative working-set status (HasPendingChanges,
// which queries dolt_status) and skips staging + commit when nothing is pending.
// These tests assert on whether DOLT_COMMIT is issued — the actual discriminator
// (HEAD never advances on an empty commit either way, so HEAD is not a valid
// signal; the bug is the wasted commit attempt and its warning).

// matchPendingQuery matches the dolt_status query used by HasPendingChanges
// (the global "anything pending?" fast-path check, which joins dolt_ignore).
var matchPendingQuery = regexp.MustCompile(`SELECT COUNT\(\*\) FROM dolt_status s`)

// matchStagedQuery matches the dolt_status query used by HasStagedChanges (the
// precise "anything actually staged?" check issued after DOLT_ADD).
var matchStagedQuery = regexp.MustCompile(`SELECT COUNT\(\*\) FROM dolt_status WHERE staged = 1`)

func TestStageAndCommitSkipsWhenNothingPending(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// dolt_status reports ZERO committable changes — the idempotent no-op case.
	mock.ExpectQuery(matchPendingQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// No ExpectExec for DOLT_ADD / DOLT_COMMIT: if StageAndCommit issues either,
	// sqlmock fails the test with "call not expected".

	err = StageAndCommit(context.Background(), db, map[string]bool{"metadata": true}, "idempotent heartbeat", "tester")
	if err != nil {
		t.Fatalf("StageAndCommit returned error on a clean no-op: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("StageAndCommit issued a commit when nothing was pending (the bug): %v", err)
	}
}

func TestStageAndCommitCommitsWhenPending(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// dolt_status reports a committable change — a real write.
	mock.ExpectQuery(matchPendingQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	// The real path must stage the dirty table...
	mock.ExpectExec(`CALL DOLT_ADD\(\?\)`).
		WithArgs("metadata").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// ...confirm something is actually staged...
	mock.ExpectQuery(matchStagedQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	// ...and commit.
	mock.ExpectExec(`CALL DOLT_COMMIT`).
		WithArgs("real change", "tester").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = StageAndCommit(context.Background(), db, map[string]bool{"metadata": true}, "real change", "tester")
	if err != nil {
		t.Fatalf("StageAndCommit returned error on a real change: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("StageAndCommit failed to stage+commit a real change: %v", err)
	}
}

// TestStageAndCommitSkipsWhenOnlyUnrelatedTableDirty is a regression test for
// the second, subtler half of the empty-commit bug: the dirty-tracked tables
// turn out clean (idempotent INSERT IGNORE label/dependency write) while some
// UNRELATED table (config, issues, …) is concurrently dirty in the working set.
//
// The global HasPendingChanges fast-path returns true (the unrelated table is
// pending), so StageAndCommit proceeds to DOLT_ADD its dirty-tracked table — but
// that stages nothing, and without the post-staging HasStagedChanges guard the
// '-m' commit would fire and Dolt would log "nothing to commit". The fix must
// detect the empty staged set and skip the commit.
func TestStageAndCommitSkipsWhenOnlyUnrelatedTableDirty(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Global status: something IS pending (an unrelated table like config).
	mock.ExpectQuery(matchPendingQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	// We stage only the dirty-tracked table ("labels"), which is clean here.
	mock.ExpectExec(`CALL DOLT_ADD\(\?\)`).
		WithArgs("labels").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Staged set is EMPTY — the guard must skip the commit. No ExpectExec for
	// DOLT_COMMIT: if StageAndCommit issues it, sqlmock fails with "not expected".
	mock.ExpectQuery(matchStagedQuery.String()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	err = StageAndCommit(context.Background(), db, map[string]bool{"labels": true}, "idempotent label add", "tester")
	if err != nil {
		t.Fatalf("StageAndCommit returned error on a no-op with unrelated dirty table: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("StageAndCommit issued an empty commit when only an unrelated table was dirty (the bug): %v", err)
	}
}
