package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// errWriteBodyPanic is the sentinel a test write body panics with.
var errWriteBodyPanic = errors.New("write body panic")

// TestCommitWriteTxPanicRollsBackAndPropagates: a write body that panics must
// roll its transaction back as the panic unwinds out of commitWriteTxOn, so the
// panic reaches the caller and the session is returned. On a pinned connection
// (withDeleteFence) an open Tx would otherwise block the deferred conn.Close
// forever.
func TestCommitWriteTxPanicRollsBackAndPropagates(t *testing.T) {
	cases := []struct {
		name   string
		expect func(mock sqlmock.Sqlmock)
		run    func(ctx context.Context, store *DoltStore, body func(*sql.Tx) error)
	}{
		{
			name: "pool",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectRollback()
			},
			run: func(ctx context.Context, store *DoltStore, body func(*sql.Tx) error) {
				_, _ = store.commitWriteTx(ctx, body)
			},
		},
		{
			name: "pinned_connection",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("SELECT GET_LOCK").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
				mock.ExpectBegin()
				mock.ExpectRollback()
				mock.ExpectQuery("SELECT RELEASE_LOCK").WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
			},
			run: func(ctx context.Context, store *DoltStore, body func(*sql.Tx) error) {
				_ = store.withDeleteFence(ctx, "bd-1", body)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			tc.expect(mock)
			store := &DoltStore{db: db}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			recovered := make(chan any, 1)
			go func() {
				defer func() { recovered <- recover() }()
				tc.run(ctx, store, func(*sql.Tx) error { panic(errWriteBodyPanic) })
			}()
			select {
			case r := <-recovered:
				if r != errWriteBodyPanic {
					t.Fatalf("recovered %v, want the write body's panic", r)
				}
			case <-time.After(5 * time.Second):
				// Cancel so Tx.awaitDone rolls the leaked Tx back and the
				// goroutine can finish instead of leaking past the test.
				cancel()
				t.Fatal("a panicking write body hung instead of propagating to the caller")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("panic path SQL: %v", err)
			}
			if inUse := db.Stats().InUse; inUse != 0 {
				t.Errorf("db.Stats().InUse = %d after the panic, want 0", inUse)
			}
		})
	}
}
