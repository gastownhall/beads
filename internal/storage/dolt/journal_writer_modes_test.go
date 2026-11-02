package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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
