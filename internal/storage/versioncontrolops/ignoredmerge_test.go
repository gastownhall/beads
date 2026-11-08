package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// stompRefusal is the pinned engine's refusal as the embedded pull returns it.
var stompRefusal = errors.New("merge origin/main: Error 1105: error: local changes would be stomped by merge:\n\tevents\n Please commit your changes before you merge.")

const (
	mergeBaseQuery = "SELECT DOLT_MERGE_BASE('HEAD', ?), DOLT_HASHOF(?)"
	ignoredQuery   = "SELECT table_name FROM dolt_status_ignored WHERE ignored"
)

// newIgnoredMergeMock is a single-session DBConn that matches queries exactly.
func newIgnoredMergeMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// TestSettleIgnoredTableStompEscalatesWhenContainmentIsUnknown pins that an
// unavailable containment proof only declines the no-op success: positive
// evidence that ignored working rows block the merge still classifies it, and
// the original refusal stays reachable.
func TestSettleIgnoredTableStompEscalatesWhenContainmentIsUnknown(t *testing.T) {
	cases := map[string]func(sqlmock.Sqlmock){
		"merge base unreadable": func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnError(errors.New("connection reset"))
		},
		"remote tip not contained": func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow("aaaa", "bbbb"))
		},
	}
	for name, expectProof := range cases {
		t.Run(name, func(t *testing.T) {
			db, mock := newIgnoredMergeMock(t)
			expectProof(mock)
			mock.ExpectQuery(ignoredQuery).WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("events").AddRow("wisps"))

			err := settleIgnoredTableStomp(context.Background(), db, "origin/main", stompRefusal)
			var ignoredErr *IgnoredTablesMergeError
			if !errors.As(err, &ignoredErr) {
				t.Fatalf("settle = %v; want an IgnoredTablesMergeError", err)
			}
			if !slices.Equal(ignoredErr.Tables, []string{"events"}) {
				t.Fatalf("Tables = %v, want [events]", ignoredErr.Tables)
			}
			if !errors.Is(err, stompRefusal) || err.Error() != stompRefusal.Error() {
				t.Fatalf("typed error does not preserve the original refusal: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
