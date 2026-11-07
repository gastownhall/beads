package dolt

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// The post-transaction commit stages every table of a write with one
// CALL DOLT_ADD naming them all, not one call per table. The staged set is the
// same; only the number of statements changes.

// expectOneDoltAdd expects one CALL DOLT_ADD with a placeholder for each
// table, bound to the tables in order, followed by the staged-set check and the
// commit.
func expectOneDoltAdd(mock sqlmock.Sqlmock, tables []string, message string) {
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(tables)), ", ")
	args := make([]driver.Value, len(tables))
	for i, table := range tables {
		args[i] = table
	}
	mock.ExpectQuery("^" + regexp.QuoteMeta("CALL DOLT_ADD("+placeholders+")") + "$").
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
		WithArgs(message, " <>").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
}

// TestDoltAddAndCommitStagesEveryTableInOneDoltAdd covers the two ways a
// write's staging list grows past one table: the list the write names (a
// dependency add that recorded an event and flipped is_blocked stages three
// tables), and the versioned-history tables added to it when history is on.
// Each must stage with one DOLT_ADD, so the commit sends three statements in
// all: DOLT_ADD, the staged-set check and DOLT_COMMIT.
func TestDoltAddAndCommitStagesEveryTableInOneDoltAdd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tables  []string
		history bool
		staged  []string
	}{
		{
			name:   "dependency add",
			tables: []string{"dependencies", "events", "issues"},
			staged: []string{"dependencies", "events", "issues"},
		},
		{
			name:    "label add with versioned history on",
			tables:  []string{"events", "labels"},
			history: true,
			staged:  []string{"events", "labels", "issue_versions", "store_epoch", "issues"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, counts := vhCountedStore(t)
			store.SetVersionedHistoryEnabled(tc.history)
			const message = "bd: test commit"
			expectOneDoltAdd(mock, tc.staged, message)

			err := store.doltAddAndCommit(context.Background(), tc.tables, message)
			sent := counts.Snapshot().Total()
			if err != nil {
				t.Fatalf("doltAddAndCommit(%v) = %v after %d statement(s); want one DOLT_ADD naming %v", tc.tables, err, sent, tc.staged)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
			if sent != 3 {
				t.Fatalf("doltAddAndCommit(%v) sent %d statements, want 3: one DOLT_ADD, the staged-set check, DOLT_COMMIT", tc.tables, sent)
			}
		})
	}
}
