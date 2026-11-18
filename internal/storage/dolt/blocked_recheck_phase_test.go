package dolt

import (
	"context"
	"errors"
	"slices"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// scopeAndRecordDependent scopes both of tx's SQL transactions for the
// blocked recheck, as runDoltTransactionRecording does, and records one
// dependent on each, as a close on each plane would.
func scopeAndRecordDependent(t *testing.T, tx *doltTransaction) {
	t.Helper()
	t.Cleanup(issueops.ScopeBlockedRecheckTransaction(tx.regularTx))
	t.Cleanup(issueops.ScopeBlockedRecheckTransaction(tx.ignoredTx))
	issueops.NoteStatusChangeBlockedRecheck(tx.regularTx, "rp-a", string(types.StatusClosed), "actor", []string{"rp-c"}, nil)
	issueops.NoteStatusChangeBlockedRecheck(tx.ignoredTx, "rp-wa", string(types.StatusClosed), "actor", nil, []string{"rp-wc"})
}

// TestFinishDoltTransactionRecordingKeepsRecheckWhenDoltCommitFails pins the
// RunInTransaction half of #6716 where the regular SQL commit lands and the
// Dolt commit after it fails. The rows are committed, the error is
// ErrCommitIndeterminate and the write is never replayed, so the recorded
// dependents must still reach the post-commit recheck.
func TestFinishDoltTransactionRecordingKeepsRecheckWhenDoltCommitFails(t *testing.T) {
	store, conn, tx, regularMock, ignoredMock := newTransactionPhaseFixture(t)
	scopeAndRecordDependent(t, tx)
	tx.dirty.MarkDirty("issues")
	regularMock.ExpectCommit()
	regularMock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	regularMock.ExpectQuery("CALL DOLT_ADD\\(\\?\\)").WithArgs("issues").
		WillReturnError(errors.New("invalid connection"))
	ignoredMock.ExpectRollback()

	pending, err := store.finishDoltTransactionRecording(context.Background(), conn, tx, "test: dolt commit fails")
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want ErrCommitIndeterminate", err)
	}
	// Both transactions recorded "actor", so the merged recheck journals its
	// derived records as that writer's, not as no one's.
	if !slices.Equal(pending.IssueIDs, []string{"rp-c"}) || !slices.Equal(pending.WispIDs, []string{"rp-wc"}) ||
		pending.Actor != "actor" {
		t.Fatalf("pending = %+v, want the dependents and the actor recorded on both transactions", pending)
	}
	requireTransactionPhaseMocks(t, regularMock, ignoredMock)
}

// TestFinishDoltTransactionRecordingDropsRecheckWhenSQLCommitRefused is the
// other side: a regular SQL commit the server refused rolled back, so there
// is nothing committed to recheck.
func TestFinishDoltTransactionRecordingDropsRecheckWhenSQLCommitRefused(t *testing.T) {
	store, conn, tx, regularMock, ignoredMock := newTransactionPhaseFixture(t)
	scopeAndRecordDependent(t, tx)
	regularMock.ExpectCommit().WillReturnError(&mysql.MySQLError{Number: 1213, Message: "serialization failure"})
	ignoredMock.ExpectRollback()

	pending, err := store.finishDoltTransactionRecording(context.Background(), conn, tx, "test: sql commit refused")
	if err == nil || errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want a determinate commit failure", err)
	}
	if !pending.Empty() {
		t.Fatalf("pending = %+v, want nothing after a refused SQL commit", pending)
	}
	requireTransactionPhaseMocks(t, regularMock, ignoredMock)
}
