package dolt

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// #7839: after a CLI-route pull leaves a conflict the resolver declined, the
// merge must be aborted on the store's pool, so the store is not wedged on an
// open merge. These tests drive abortOpenMergeAfterCLIPull directly against a
// sqlmock pool, the way the store's own pool sees it.

func newAbortTestStore(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{db: db}, mock
}

func TestAbortOpenMergeAfterCLIPullAbortsDeclinedMerge(t *testing.T) {
	s, mock := newAbortTestStore(t)
	mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
		WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(true))
	mock.ExpectQuery("CALL DOLT_MERGE('--abort')").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))

	if err := s.abortOpenMergeAfterCLIPull(context.Background()); err != nil {
		t.Fatalf("abortOpenMergeAfterCLIPull: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected the open merge to be aborted: %v", err)
	}
}

func TestAbortOpenMergeAfterCLIPullNoOpWithoutOpenMerge(t *testing.T) {
	s, mock := newAbortTestStore(t)
	mock.ExpectQuery("SELECT is_merging FROM dolt_merge_status").
		WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))

	// No abort is expected: any unexpected CALL fails the query and the test.
	if err := s.abortOpenMergeAfterCLIPull(context.Background()); err != nil {
		t.Fatalf("abortOpenMergeAfterCLIPull with no open merge: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected calls: %v", err)
	}
}
