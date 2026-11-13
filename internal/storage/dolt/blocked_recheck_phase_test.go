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

// scopeAndRecordDependent scopes tx for the blocked recheck, as
// runDoltTransactionRecording does, and records one durable and one wisp
// dependent on it, as a close on each plane would.
func scopeAndRecordDependent(t *testing.T, tx *doltTransaction) {
	t.Helper()
	t.Cleanup(issueops.ScopeBlockedRecheckTransaction(tx.tx))
	issueops.NoteStatusChangeBlockedRecheck(tx.tx, "rp-a", string(types.StatusClosed), []string{"rp-c"}, nil)
	issueops.NoteStatusChangeBlockedRecheck(tx.tx, "rp-wa", string(types.StatusClosed), nil, []string{"rp-wc"})
}

// TestFinishDoltTransactionRecordingKeepsRecheckWhenDoltCommitFails pins the
// RunInTransaction half of #6716 where the SQL commit lands and the Dolt
// commit after it fails. The rows are committed, the error is
// ErrCommitIndeterminate and the write is never replayed, so the recorded
// dependents must still reach the post-commit recheck.
func TestFinishDoltTransactionRecordingKeepsRecheckWhenDoltCommitFails(t *testing.T) {
	store, conn, tx, mock := newTransactionPhaseFixture(t)
	scopeAndRecordDependent(t, tx)
	tx.dirty.MarkDirty("issues")
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("CALL DOLT_ADD\\(\\?\\)").WithArgs("issues").
		WillReturnError(errors.New("invalid connection"))

	pending, err := store.finishDoltTransactionRecording(context.Background(), conn, tx, "test: dolt commit fails")
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want ErrCommitIndeterminate", err)
	}
	if !slices.Equal(pending.IssueIDs, []string{"rp-c"}) || !slices.Equal(pending.WispIDs, []string{"rp-wc"}) {
		t.Fatalf("pending = %+v, want the dependents recorded on the transaction", pending)
	}
	requireTransactionPhaseMocks(t, mock)
}

// TestFinishDoltTransactionRecordingDropsRecheckWhenSQLCommitRefused is the
// other side: a SQL commit the server refused rolled back, so there is
// nothing committed to recheck.
func TestFinishDoltTransactionRecordingDropsRecheckWhenSQLCommitRefused(t *testing.T) {
	store, conn, tx, mock := newTransactionPhaseFixture(t)
	scopeAndRecordDependent(t, tx)
	mock.ExpectCommit().WillReturnError(&mysql.MySQLError{Number: 1213, Message: "serialization failure"})

	pending, err := store.finishDoltTransactionRecording(context.Background(), conn, tx, "test: sql commit refused")
	if err == nil || errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want a determinate commit failure", err)
	}
	if !pending.Empty() {
		t.Fatalf("pending = %+v, want nothing after a refused SQL commit", pending)
	}
	requireTransactionPhaseMocks(t, mock)
}
