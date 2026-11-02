package dolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// TestJournalWriterLock_OnlyServerJournaledWritesTakeTheLock pins where the
// journal-writer lock applies (gastownhall/beads#6117): a server-mode store
// with the journal on takes it around each write transaction, and an embedded
// store or a journal-off store sends no lock SQL at all, on the funnel and on
// a raw journal-scoped transaction alike. sqlmock fails on any statement it
// was not told to expect, so the off cases prove the absence.
func TestJournalWriterLock_OnlyServerJournaledWritesTakeTheLock(t *testing.T) {
	for _, tc := range []struct {
		name       string
		serverMode bool
		journalOn  bool
		wantLock   bool
	}{
		{name: "embedded journal on", serverMode: false, journalOn: true},
		{name: "server journal off", serverMode: true, journalOn: false},
		{name: "server journal on", serverMode: true, journalOn: true, wantLock: true},
	} {
		for _, route := range []struct {
			name string
			run  func(context.Context, *DoltStore) error
		}{
			{"funnel", func(ctx context.Context, s *DoltStore) error {
				_, err := s.commitWriteTxOn(ctx, s.db, func(*sql.Tx) error { return nil })
				return err
			}},
			{"raw", func(ctx context.Context, s *DoltStore) error {
				tx, done, err := s.beginJournalScopedTx(ctx)
				if err != nil {
					return err
				}
				defer done()
				return s.commitSQLTx(ctx, "commit raw route", tx)
			}},
		} {
			t.Run(tc.name+"/"+route.name, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New: %v", err)
				}
				t.Cleanup(func() { _ = db.Close() })
				store := &DoltStore{db: db, database: "beads", serverMode: tc.serverMode}
				store.SetEventsJournalEnabled(tc.journalOn)

				if tc.wantLock {
					mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs("bd_events_seq:beads", sqlmock.AnyArg()).
						WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(1))
				}
				mock.ExpectBegin()
				mock.ExpectCommit()
				if tc.wantLock {
					mock.ExpectQuery(`SELECT RELEASE_LOCK\(`).WithArgs("bd_events_seq:beads").
						WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
				}

				if err := route.run(context.Background(), store); err != nil {
					t.Fatalf("%s: %v", route.name, err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("lock SQL: %v", err)
				}
			})
		}
	}
}

// TestJournalWriterLock_CleanupEndsTheTransactionBeforeUnlock pins how a
// journal-admitted transaction gives the lock up on each way out
// (gastownhall/beads#6117): RELEASE_LOCK is sent, and the session kept, only
// after the transaction has committed or rolled back. When its end is not
// proven (a rollback or commit that failed without a guaranteed server-side
// rollback, or a cancellation whose rollback database/sql ran on its own), no
// RELEASE_LOCK is sent on a session that may still hold the transaction open:
// the session is discarded, and the server drops the lock with it. sqlmock
// matches in order, so a met expectation list proves the end came before the
// unlock; the RELEASE_LOCK expectation is always last, and left unmet when no
// unlock may be sent.
func TestJournalWriterLock_CleanupEndsTheTransactionBeforeUnlock(t *testing.T) {
	errBody := errors.New("body failed")
	routes := []struct {
		name   string
		branch bool // the route reads the active branch after admission
		run    func(ctx context.Context, s *DoltStore, body func() error) error
	}{
		{name: "funnel", run: func(ctx context.Context, s *DoltStore, body func() error) error {
			_, err := s.commitWriteTxOn(ctx, s.db, func(*sql.Tx) error { return body() })
			return err
		}},
		{name: "raw", run: func(ctx context.Context, s *DoltStore, body func() error) error {
			tx, done, err := s.beginJournalScopedTx(ctx)
			if err != nil {
				return err
			}
			defer done()
			if err := body(); err != nil {
				return err
			}
			return s.commitSQLTx(ctx, "commit raw route", tx)
		}},
		{name: "transaction", branch: true, run: func(ctx context.Context, s *DoltStore, body func() error) error {
			_, err := s.runDoltTransactionRecording(ctx, "", func(storage.Transaction) error { return body() })
			return err
		}},
	}
	exits := []struct {
		name        string
		end         func(sqlmock.Sqlmock)
		body        func(cancel context.CancelFunc, mock sqlmock.Sqlmock) error
		wantRelease bool
		wantPanic   bool
	}{
		{name: "commit", wantRelease: true,
			end:  func(m sqlmock.Sqlmock) { m.ExpectCommit() },
			body: func(context.CancelFunc, sqlmock.Sqlmock) error { return nil }},
		{name: "error", wantRelease: true,
			end:  func(m sqlmock.Sqlmock) { m.ExpectRollback() },
			body: func(context.CancelFunc, sqlmock.Sqlmock) error { return errBody }},
		{name: "panic", wantRelease: true, wantPanic: true,
			end:  func(m sqlmock.Sqlmock) { m.ExpectRollback() },
			body: func(context.CancelFunc, sqlmock.Sqlmock) error { panic("body panicked") }},
		{name: "cancel",
			end: func(m sqlmock.Sqlmock) { m.ExpectRollback() },
			body: func(cancel context.CancelFunc, m sqlmock.Sqlmock) error {
				cancel()
				// database/sql rolls a canceled transaction back on its own;
				// wait for that ROLLBACK so the cleanup finds the tx done.
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if err := m.ExpectationsWereMet(); err != nil && strings.Contains(err.Error(), "RELEASE_LOCK") {
						return context.Canceled
					}
					time.Sleep(time.Millisecond)
				}
				return errors.New("database/sql never rolled the canceled transaction back")
			}},
		{name: "rollback fails",
			end:  func(m sqlmock.Sqlmock) { m.ExpectRollback().WillReturnError(errors.New("rollback lost")) },
			body: func(context.CancelFunc, sqlmock.Sqlmock) error { return errBody }},
		{name: "commit lost",
			end:  func(m sqlmock.Sqlmock) { m.ExpectCommit().WillReturnError(errors.New("commit lost")) },
			body: func(context.CancelFunc, sqlmock.Sqlmock) error { return nil }},
		{name: "commit refused", wantRelease: true,
			end: func(m sqlmock.Sqlmock) {
				m.ExpectCommit().WillReturnError(&mysql.MySQLError{Number: 1213, Message: "serialization failure"})
			},
			body: func(context.CancelFunc, sqlmock.Sqlmock) error { return nil }},
	}
	for _, route := range routes {
		for _, exit := range exits {
			t.Run(route.name+"/"+exit.name, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatalf("sqlmock.New: %v", err)
				}
				t.Cleanup(func() { _ = db.Close() })
				store := &DoltStore{db: db, database: "beads", serverMode: true}
				store.SetEventsJournalEnabled(true)

				mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs("bd_events_seq:beads", sqlmock.AnyArg()).
					WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(1))
				if route.branch {
					mock.ExpectQuery(`SELECT active_branch\(\)`).
						WillReturnRows(sqlmock.NewRows([]string{"branch"}).AddRow("main"))
				}
				mock.ExpectBegin()
				exit.end(mock)
				mock.ExpectQuery(`SELECT RELEASE_LOCK\(`).WithArgs("bd_events_seq:beads").
					WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				panicked := func() (p any) {
					defer func() { p = recover() }()
					_ = route.run(ctx, store, func() error { return exit.body(cancel, mock) })
					return nil
				}()
				if (panicked != nil) != exit.wantPanic {
					t.Fatalf("panic = %v, want panic %v", panicked, exit.wantPanic)
				}

				met := mock.ExpectationsWereMet()
				open := db.Stats().OpenConnections
				if exit.wantRelease {
					if met != nil {
						t.Fatalf("want the transaction ended, then RELEASE_LOCK: %v", met)
					}
					if open != 1 {
						t.Fatalf("open connections = %d, want the released session kept", open)
					}
					return
				}
				if met == nil {
					t.Fatalf("RELEASE_LOCK was sent on a session whose transaction may still be open")
				}
				if !strings.Contains(met.Error(), "RELEASE_LOCK") {
					t.Fatalf("want every statement before RELEASE_LOCK sent: %v", met)
				}
				if open != 0 {
					t.Fatalf("open connections = %d, want the session discarded", open)
				}
			})
		}
	}
}
