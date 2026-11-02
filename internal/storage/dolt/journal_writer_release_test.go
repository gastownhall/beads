package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// TestWriteTxConn_FailedReleaseDiscardsSession pins that a RELEASE_LOCK the
// server does not confirm does not hand the session back to the pool
// (gastownhall/beads#6117): locks are per session and reentrant, so a pooled
// session that may still own the lock would keep every other journaled writer
// out. Only an answer of 1 confirms the release; an error, 0 (the session did
// not own it) and NULL (no such lock) all discard. The write itself had
// already committed, so it still succeeds.
//
// Ported from PR #7645 (Raymond W. Ko), adapted to the SELECT RELEASE_LOCK
// result check and extended to the 0 and NULL answers.
func TestWriteTxConn_FailedReleaseDiscardsSession(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(*sqlmock.ExpectedQuery)
	}{
		{"error", func(q *sqlmock.ExpectedQuery) {
			q.WillReturnError(&mysql.MySQLError{Number: 1317, Message: "Query execution was interrupted"})
		}},
		{"not the owner", func(q *sqlmock.ExpectedQuery) {
			q.WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(0))
		}},
		{"no such lock", func(q *sqlmock.ExpectedQuery) {
			q.WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(nil))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db, mock := newAdmissionMockStore(t)
			mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs("bd_events_seq:beads", sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(1))
			mock.ExpectBegin()
			mock.ExpectCommit()
			tc.release(mock.ExpectQuery(`SELECT RELEASE_LOCK\(`).WithArgs("bd_events_seq:beads"))

			runs := 0
			_, err := store.commitWriteTxWith(context.Background(), store.db, journalTxWriter, time.Now().Add(time.Minute), func(*sql.Tx) error {
				runs++
				return nil
			})
			if err != nil {
				t.Fatalf("a committed write failed on its unlock: %v", err)
			}
			if runs != 1 {
				t.Fatalf("body ran %d times, want once", runs)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("lock lifecycle: %v", err)
			}
			if got := db.Stats().OpenConnections; got != 0 {
				t.Fatalf("an unconfirmed RELEASE_LOCK returned %d live session(s) to the pool; want the session discarded", got)
			}
		})
	}
}

// TestWriteTxConn_ReleaseIsBounded pins the cleanup deadline
// (gastownhall/beads#6117): release runs detached from the caller's (here
// already canceled) context, but a server that stalls on RELEASE_LOCK must not
// hold the caller past the release timeout (3s), and the unconfirmed session
// is discarded.
//
// Ported from PR #7645 (Raymond W. Ko), adapted to the store's admission.
func TestWriteTxConn_ReleaseIsBounded(t *testing.T) {
	store, db, mock := newAdmissionMockStore(t)
	mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs("bd_events_seq:beads", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(1))
	mock.ExpectQuery(`SELECT RELEASE_LOCK\(`).WithArgs("bd_events_seq:beads").
		WillDelayFor(6 * time.Second).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	ctx, cancel := context.WithCancel(context.Background())
	adm, err := store.admitJournalWriter(ctx, store.db, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	cancel()
	start := time.Now()
	adm.release()
	if elapsed := time.Since(start); elapsed > 3*time.Second+500*time.Millisecond {
		t.Fatalf("release after cancellation took %s, beyond the 3s cleanup budget", elapsed)
	}
	if got := db.Stats().OpenConnections; got != 0 {
		t.Fatalf("a timed-out RELEASE_LOCK returned %d live session(s) to the pool; want the session discarded", got)
	}
}

// TestJournalWriterLock_UncertainAcquisitionDiscardsSession pins the other
// unknown lock state (gastownhall/beads#6117): a GET_LOCK that fails or
// answers NULL may or may not have left the lock on the session, so the
// writer is refused, no RELEASE_LOCK is guessed at, no transaction begins,
// and the session is discarded rather than pooled.
func TestJournalWriterLock_UncertainAcquisitionDiscardsSession(t *testing.T) {
	errLost := errors.New("connection lost mid GET_LOCK")
	for _, tc := range []struct {
		name    string
		acquire func(*sqlmock.ExpectedQuery)
	}{
		{"error", func(q *sqlmock.ExpectedQuery) { q.WillReturnError(errLost) }},
		{"NULL", func(q *sqlmock.ExpectedQuery) {
			q.WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(nil))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db, mock := newAdmissionMockStore(t)
			tc.acquire(mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs("bd_events_seq:beads", sqlmock.AnyArg()))

			ran := false
			_, err := store.commitWriteTxWith(context.Background(), store.db, journalTxWriter, time.Now().Add(time.Minute), func(*sql.Tx) error {
				ran = true
				return nil
			})
			if err == nil {
				t.Fatal("a writer whose GET_LOCK answer was lost was admitted")
			}
			if ran {
				t.Fatal("the body ran without a confirmed lock")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("lock lifecycle: %v", err)
			}
			if got := db.Stats().OpenConnections; got != 0 {
				t.Fatalf("an uncertain GET_LOCK returned %d live session(s) to the pool; want the session discarded", got)
			}
		})
	}
}
