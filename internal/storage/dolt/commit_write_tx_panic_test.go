package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// errWriteBodyPanic is the sentinel a panicking write body throws; each panic
// subtest recovers the panic and asserts it is this same value.
var errWriteBodyPanic = errors.New("write body panic")

// runPanickingBody runs a body that panics with errWriteBodyPanic through
// run in a goroutine whose recover feeds a buffered channel, waits up to
// timeout, and on a hang cancels ctx (so Tx.awaitDone rolls the leaked
// transaction back and the goroutine can exit) before failing the test
// instead of leaking it.
func runPanickingBody(t *testing.T, ctx context.Context, cancel context.CancelFunc, timeout time.Duration, run func(fn func(*sql.Tx) error)) {
	t.Helper()

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		run(func(*sql.Tx) error { panic(errWriteBodyPanic) })
	}()
	select {
	case r := <-recovered:
		if r != errWriteBodyPanic {
			t.Fatalf("recovered %v, want the write body's panic %v", r, errWriteBodyPanic)
		}
	case <-time.After(timeout):
		cancel()
		t.Fatalf("a panicking write body hung instead of propagating to the caller within %s", timeout)
	}
}

// TestCommitWriteTxPanicRollsBackAndPropagates: a write body that panics must
// roll its transaction back as the panic unwinds out of commitWriteTxOn, so
// the panic reaches the caller and the underlying session is returned. On the
// shared pool an open Tx keeps its connection checked out; on a pinned
// *sql.Conn (withDeleteFence) the caller's deferred conn.Close would block
// forever on the open Tx and swallow the panic.
func TestCommitWriteTxPanicRollsBackAndPropagates(t *testing.T) {
	t.Run("pool", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mock.ExpectBegin()
		mock.ExpectRollback()

		store := &DoltStore{db: db}
		runPanickingBody(t, ctx, cancel, 5*time.Second, func(fn func(*sql.Tx) error) {
			_, _ = store.commitWriteTx(ctx, fn)
		})

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("panic path SQL: %v", err)
		}
		if inUse := db.Stats().InUse; inUse != 0 {
			t.Errorf("db.Stats().InUse = %d after the panic, want 0", inUse)
		}
	})
}
