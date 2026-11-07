package schema

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestLatestVersionIncludesMigration0075 pins the main slot the is_blocked
// repair marker claims (gastownhall/beads#7037), directly above the id
// tombstone tables' (0074), and that the marker carries its repair.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (is_blocked repair marker)", got, want)
	}
	if _, ok := preMigrationRepairs[repairKey{mainSource.cursorTable, want}]; !ok {
		t.Fatalf("main 0075 has no pre-migration repair registered; the marker alone records nothing")
	}
}

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot that
// performs the repair, directly above the events restore's (ignored 0031).
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const want = 32
	if got := LatestIgnoredVersion(); got != want {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (is_blocked repair)", got, want)
	}
	if want != isBlockedRepairIgnoredVersion {
		t.Fatalf("isBlockedRepairIgnoredVersion = %d, want %d", isBlockedRepairIgnoredVersion, want)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, want}]; !ok {
		t.Fatalf("ignored 0032 has no pre-migration repair registered; the marker alone repairs nothing")
	}
}

// TestMigration0075MarkerIsSeenTouchingIssuesAndMetadata pins the raw-text
// declaration the pending-migration dirty-table guard relies on: the marker's
// repair writes metadata (the owed key) and issues (the recomputed flags), so
// a store with uncommitted edits to either refuses the pass typed, before any
// repair write. The body itself is inert.
func TestMigration0075MarkerIsSeenTouchingIssuesAndMetadata(t *testing.T) {
	const name = "0075_repair_is_blocked_after_0059.up.sql"
	upSQL, err := MigrationSQL(name)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) = %v", name, err)
	}
	for _, table := range []string{"metadata", "issues"} {
		if !migrationSQLTouchesTable(upSQL, table) {
			t.Errorf("pending-migration guard does not see %s in %s; uncommitted edits to it would ride the repair's commit", table, name)
		}
	}
	if !strings.Contains(upSQL, "SELECT 1;") {
		t.Errorf("%s body is not the inert marker", name)
	}
}

// TestMigration0032MarkerTouchesNoTrackedTable pins the inverse for the
// clone-local half: its header names no table next to a write verb, because
// the ignored-series dirty guard fires mid-pass against the working set a
// clone already holds. A tracked table named here would abort the pass of
// every clone holding uncommitted rows in it; the tables the repair commits
// are guarded before the pass instead (refuseDirtyIsBlockedRepairInputs).
func TestMigration0032MarkerTouchesNoTrackedTable(t *testing.T) {
	const name = "0032_repair_is_blocked_after_0059.up.sql"
	upSQL, err := IgnoredMigrationSQL(name)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", name, err)
	}
	for _, table := range []string{"metadata", "issues", "dependencies", "wisps", "wisp_dependencies", "events"} {
		if migrationSQLTouchesTable(upSQL, table) {
			t.Errorf("pending-ignored guard sees %s in %s; a clone holding uncommitted %s rows would abort its pass at this marker", table, name, table)
		}
	}
	if !strings.Contains(upSQL, "SELECT 1;") {
		t.Errorf("%s body is not the inert marker", name)
	}
}

// TestRearmIgnoredIsBlockedRepairDeletesTheCursorSuffix pins the re-arm's
// shape: one DELETE of every ignored cursor row from 0032 up, so a later
// ignored step cannot hide 0032 behind a higher MAX(version) and every step
// above it replays; rows below 0032 are kept, so a lower floor stays where
// it is; and a clone without the cursor table is left alone, since its
// pass runs the whole series anyway.
func TestRearmIgnoredIsBlockedRepairDeletesTheCursorSuffix(t *testing.T) {
	tableProbe := regexp.QuoteMeta("SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES")
	for _, exists := range []bool{true, false} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		count := 0
		if exists {
			count = 1
		}
		mock.ExpectQuery(tableProbe).WithArgs(ignoredSource.cursorTable).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
		if exists {
			mock.ExpectExec(regexp.QuoteMeta("DELETE FROM ignored_schema_migrations WHERE version >= ?")).
				WithArgs(isBlockedRepairIgnoredVersion).
				WillReturnResult(sqlmock.NewResult(0, 2))
		}
		if err := rearmIgnoredIsBlockedRepair(context.Background(), db); err != nil {
			t.Fatalf("cursor table present=%v: rearmIgnoredIsBlockedRepair: %v", exists, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("cursor table present=%v: %v", exists, err)
		}
		_ = db.Close()
	}
}

// TestCommittedIsBlockedRepairCountsARepairThatLeftHEAD pins what the owed
// repair adds to MigrateUp's total once the ignored runner has returned:
// nothing, and no probe, when the pass found nothing owed; 1 once the key has
// left HEAD, which only the repair commit does; 0 while the key is still
// committed (a pass stopped short of that commit); and 1 when the probe fails,
// the direction in which a server-mode open rebuilds the pool it pinned before
// the pass instead of serving pre-repair reads (be-itm5).
func TestCommittedIsBlockedRepairCountsARepairThatLeftHEAD(t *testing.T) {
	headProbe := regexp.QuoteMeta("SHOW TABLES AS OF 'HEAD' LIKE 'metadata'")
	for _, tc := range []struct {
		name   string
		owed   bool
		expect func(sqlmock.Sqlmock)
		want   int
	}{
		{name: "nothing owed", expect: func(sqlmock.Sqlmock) {}, want: 0},
		{name: "key left HEAD", owed: true, expect: expectIsBlockedRepairNotOwed, want: 1},
		{name: "key still at HEAD", owed: true, expect: func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(headProbe).
				WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("metadata"))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = ?")).
				WithArgs(isBlockedRepairOwedKey).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		}, want: 0},
		{name: "probe fails", owed: true, expect: func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(headProbe).WillReturnError(errors.New("connection lost"))
		}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			tc.expect(mock)
			if got := committedIsBlockedRepair(context.Background(), db, tc.owed); got != tc.want {
				t.Errorf("committedIsBlockedRepair(owed=%v) = %d, want %d", tc.owed, got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet SQL expectations: %v", err)
			}
		})
	}
}
