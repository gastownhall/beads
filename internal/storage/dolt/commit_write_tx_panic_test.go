package dolt

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestCommitWriteTx_PanicRollsBackAndPropagates pins that a write body that
// panics rolls its transaction back and the panic reaches the caller. On the
// pool the open Tx would otherwise leak its session; on a pinned *sql.Conn
// (withDeleteFence) the deferred conn.Close would wait for the open Tx
// forever, so the panic would never surface at all.
func TestCommitWriteTx_PanicRollsBackAndPropagates(t *testing.T) {
	panicky := func(*sql.Tx) error { panic("write body panic") }
	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		run    func(*DoltStore)
	}{
		{
			name: "pool",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectRollback()
			},
			run: func(store *DoltStore) {
				_, _ = store.commitWriteTx(context.Background(), panicky)
			},
		},
		{
			name: "pinned connection",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
					WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
				mock.ExpectBegin()
				mock.ExpectRollback()
				mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
					WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
			},
			run: func(store *DoltStore) {
				_ = store.withDeleteFence(context.Background(), "bd-1", panicky)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := &DoltStore{db: db, database: "proj_a"}
			tc.expect(mock)

			recovered := make(chan any, 1)
			go func() {
				defer func() { recovered <- recover() }()
				tc.run(store)
			}()
			select {
			case got := <-recovered:
				if got != "write body panic" {
					t.Fatalf("recovered %v, want the write body's panic", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a panicking write body hung in the deferred connection release instead of propagating")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("panic path SQL: %v", err)
			}
			if got := store.db.Stats().InUse; got != 0 {
				t.Fatalf("a panicking write body left %d session(s) in use, want the session released", got)
			}
		})
	}
}
