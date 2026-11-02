package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestJournalWriterLock_SetupRetriesShareOneAdmissionDeadline pins the
// admission deadline of the setup-retrying transaction routes
// (gastownhall/beads#6117): it is fixed once when the call starts, so a setup
// retry does not restart it.
func TestJournalWriterLock_SetupRetriesShareOneAdmissionDeadline(t *testing.T) {
	setupErr := &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	for _, tc := range []struct {
		name string
		run  func(s *DoltStore, attempt func(context.Context) error) error
	}{
		{"RunInTransaction", func(s *DoltStore, attempt func(context.Context) error) error {
			return s.runInTransaction(context.Background(), "test: admission deadline", func(storage.Transaction) error { return nil },
				func(ctx context.Context, _ string, fn func(storage.Transaction) error) error {
					if err := attempt(ctx); err != nil {
						return err
					}
					return fn(nil)
				})
		}},
		{"RunInIssueLifecycleTransaction", func(s *DoltStore, attempt func(context.Context) error) error {
			return s.runInIssueLifecycleTransaction(context.Background(), "test: admission deadline", func(storage.IssueLifecycleTransaction) error { return nil },
				func(ctx context.Context, fn func(*sql.Tx) error) error {
					if err := attempt(ctx); err != nil {
						return err
					}
					return nil
				})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deadlines []time.Time
			err := tc.run(&DoltStore{}, func(ctx context.Context) error {
				deadlines = append(deadlines, journalAdmitByFor(ctx, issueops.JournalWriterAdmissionBudget))
				if len(deadlines) == 1 {
					time.Sleep(20 * time.Millisecond)
					return setupErr
				}
				return nil
			})
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(deadlines) != 2 {
				t.Fatalf("attempts = %d, want 2", len(deadlines))
			}
			if !deadlines[1].Equal(deadlines[0]) {
				t.Fatalf("retry admission deadline = %s, want the first attempt's %s", deadlines[1], deadlines[0])
			}
		})
	}
}

func newAdmissionMockStore(t *testing.T) (*DoltStore, *sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db, database: "beads", serverMode: true}
	store.SetEventsJournalEnabled(true)
	return store, db, mock
}

// TestJournalWriterLock_SaturatedPoolIsBusyAtTheDeadline pins the pool wait
// of the journaled write routes against the admission deadline
// (gastownhall/beads#6117): when every pooled session is busy, the writer is
// refused busy at its deadline, its body never runs, and the caller's context
// is still live.
func TestJournalWriterLock_SaturatedPoolIsBusyAtTheDeadline(t *testing.T) {
	for _, route := range []struct {
		name string
		run  func(ctx context.Context, s *DoltStore, body func() error) error
	}{
		{"funnel", func(ctx context.Context, s *DoltStore, body func() error) error {
			_, err := s.commitWriteTxWith(ctx, s.db, journalTxWriter, time.Now().Add(100*time.Millisecond), func(*sql.Tx) error { return body() })
			return err
		}},
		{"transaction", func(ctx context.Context, s *DoltStore, body func() error) error {
			ctx = withJournalAdmitBy(ctx, 100*time.Millisecond)
			_, err := s.runDoltTransactionRecording(ctx, "", func(storage.Transaction) error { return body() })
			return err
		}},
	} {
		t.Run(route.name, func(t *testing.T) {
			store, db, _ := newAdmissionMockStore(t)
			db.SetMaxOpenConns(1)
			held, err := db.Conn(context.Background())
			if err != nil {
				t.Fatalf("pin: %v", err)
			}
			t.Cleanup(func() { _ = held.Close() })

			ran := false
			start := time.Now()
			err = route.run(context.Background(), store, func() error { ran = true; return nil })
			if ran {
				t.Fatal("the body ran without a pooled session")
			}
			if !errors.Is(err, issueops.ErrJournalWriterBusy) {
				t.Fatalf("write on a saturated pool = %v, want ErrJournalWriterBusy", err)
			}
			if waited := time.Since(start); waited > 5*time.Second {
				t.Fatalf("the write waited %s for the pool, want it bounded by its 100ms deadline", waited)
			}
		})
	}
}

// TestJournalWriterLock_BodyRunsPastTheAdmissionDeadline pins what the
// admission deadline bounds (gastownhall/beads#6117): only the wait for the
// lock. A writer admitted in time runs its body on the caller's context to
// commit, however long past the deadline that takes, and then releases.
func TestJournalWriterLock_BodyRunsPastTheAdmissionDeadline(t *testing.T) {
	for _, route := range []struct {
		name   string
		branch bool
		run    func(ctx context.Context, s *DoltStore, body func() error) error
	}{
		{name: "funnel", run: func(ctx context.Context, s *DoltStore, body func() error) error {
			_, err := s.commitWriteTxWith(ctx, s.db, journalTxWriter, time.Now().Add(50*time.Millisecond), func(*sql.Tx) error { return body() })
			return err
		}},
		{name: "transaction", branch: true, run: func(ctx context.Context, s *DoltStore, body func() error) error {
			ctx = withJournalAdmitBy(ctx, 50*time.Millisecond)
			_, err := s.runDoltTransactionRecording(ctx, "", func(storage.Transaction) error { return body() })
			return err
		}},
	} {
		t.Run(route.name, func(t *testing.T) {
			store, _, mock := newAdmissionMockStore(t)
			mock.ExpectQuery(`SELECT GET_LOCK\(`).WithArgs("bd_events_seq:beads", sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(1))
			if route.branch {
				mock.ExpectQuery(`SELECT active_branch\(\)`).
					WillReturnRows(sqlmock.NewRows([]string{"branch"}).AddRow("main"))
			}
			mock.ExpectBegin()
			mock.ExpectCommit()
			mock.ExpectQuery(`SELECT RELEASE_LOCK\(`).WithArgs("bd_events_seq:beads").
				WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

			err := route.run(context.Background(), store, func() error {
				time.Sleep(200 * time.Millisecond)
				return nil
			})
			if err != nil {
				t.Fatalf("a body running past the admission deadline failed: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("lock SQL: %v", err)
			}
		})
	}
}
