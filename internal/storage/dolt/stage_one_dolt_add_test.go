package dolt

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// The real-server cases below write this config key and stage config.
const oneDoltAddConfigKey = "test.one-dolt-add"

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
//
// The real-server cases send that one DOLT_ADD to a Dolt sql-server and read
// dolt_status afterwards, because sqlmock cannot show what Dolt stages. They
// cover the two lists where one call could stage differently from one call
// per table: a list that names dolt_ignore'd tables, and a list that names a
// table that does not exist.
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

	// leases and wisps are in dolt_ignore. Dolt drops ignored names from the
	// list before it stages, so the call stages config alone, as one call per
	// table did. wisps is dirty, so staging it would show.
	t.Run("real server: ignored tables named with config stage nothing", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		t.Cleanup(cleanup)
		ctx, cancel := testContext(t)
		t.Cleanup(cancel)

		createWisp(t, ctx, store, "one-dolt-add-wisp")
		if !tableDirty(ctx, t, store, "wisps") {
			t.Fatal("wisps is not dirty after an ephemeral create, so this case could not see wisps staged")
		}
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO config (`key`, value) VALUES (?, 'committed')", oneDoltAddConfigKey); err != nil {
			t.Fatalf("write config: %v", err)
		}

		tables := []string{"config", "leases", "wisps"}
		if err := store.doltAddAndCommit(ctx, tables, "test: one DOLT_ADD naming ignored tables"); err != nil {
			t.Fatalf("doltAddAndCommit(%v) = %v, want nil", tables, err)
		}
		var value string
		if err := store.db.QueryRowContext(ctx,
			"SELECT value FROM config AS OF 'HEAD' WHERE `key` = ?", oneDoltAddConfigKey).Scan(&value); err != nil {
			t.Fatalf("read %s AS OF HEAD: %v (the call did not stage config)", oneDoltAddConfigKey, err)
		}
		if value != "committed" {
			t.Fatalf("%s AS OF HEAD = %q, want %q", oneDoltAddConfigKey, value, "committed")
		}
		if !tableDirty(ctx, t, store, "wisps") {
			t.Fatal("wisps is clean after the commit: the call staged an ignored table and the commit published the wisp")
		}
	})

	// Dolt checks that every name is a table before it stages any of them, so
	// a missing table fails the call with nothing staged. One call per table
	// would have staged config before the call for the missing table failed.
	t.Run("real server: a missing table fails the call with nothing staged", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		t.Cleanup(cleanup)
		ctx, cancel := testContext(t)
		t.Cleanup(cancel)

		if staged := stagedTables(ctx, t, store); len(staged) != 0 {
			t.Fatalf("tables staged before the call: %v", staged)
		}
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO config (`key`, value) VALUES (?, 'not staged')", oneDoltAddConfigKey); err != nil {
			t.Fatalf("write config: %v", err)
		}
		headBefore, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("GetCurrentCommit: %v", err)
		}

		tables := []string{"config", "no_such_table"}
		if err := store.doltAddAndCommit(ctx, tables, "test: one DOLT_ADD naming a missing table"); err == nil {
			t.Fatalf("doltAddAndCommit(%v) = nil, want the server's error for no_such_table", tables)
		}
		if staged := stagedTables(ctx, t, store); len(staged) != 0 {
			t.Fatalf("tables staged after the failed call: %v, want none", staged)
		}
		headAfter, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("GetCurrentCommit: %v", err)
		}
		if headAfter != headBefore {
			t.Fatalf("HEAD moved from %s to %s after a failed DOLT_ADD", headBefore, headAfter)
		}
		if !tableDirty(ctx, t, store, "config") {
			t.Fatal("config is not dirty after the failed call: the config write was lost")
		}
	})
}

// stagedTables lists the tables dolt_status shows as staged.
func stagedTables(ctx context.Context, t *testing.T, store *DoltStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx,
		"SELECT table_name FROM dolt_status WHERE staged = 1 ORDER BY table_name")
	if err != nil {
		t.Fatalf("query staged tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan staged table: %v", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read staged tables: %v", err)
	}
	return tables
}
