package schema

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// migrateLostTombstonesUpToFirstApply walks ignoredSource.migrate with the
// cursor at raw, every sentinel present except wisp_id_tombstones, and fails
// the first migration statement it reaches. It returns what migrate wrote to
// lostTableNoticeOut.
func migrateLostTombstonesUpToFirstApply(t *testing.T, raw int) string {
	t.Helper()
	var notices bytes.Buffer
	orig := lostTableNoticeOut
	lostTableNoticeOut = &notices
	t.Cleanup(func() { lostTableNoticeOut = orig })
	stubSentinelTables(t, allSentinelTablesExcept("wisp_id_tombstones"), nil)
	stubSentinelColumns(t, map[string]bool{"leases.granted_node": true}, nil)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS ignored_schema_migrations")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM ignored_schema_migrations LIKE 'content_hash'")).
		WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
			AddRow("content_hash", "char(64)", "YES", "", nil, ""))
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", raw)
	boom := errors.New("stop here: the replay started")
	mock.ExpectExec(".*").WillReturnError(boom)

	if _, _, err := ignoredSource.migrate(context.Background(), db, 0); !errors.Is(err, boom) {
		t.Fatalf("migrate = %v, want it to reach the replay", err)
	}
	return notices.String()
}

// TestMigrateWarnsWhenItRecreatesLostTombstones pins the loud half of the
// wisp_id_tombstones heal: a clone whose cursor says ignored/0030 already ran
// but whose table is gone gets the table back EMPTY, and every retired
// wisps-plane id with it becomes creatable again — while the wisps rows may
// well have survived, so nothing else on the clone shows the loss. migrate
// says so before it replays, on the unconditional writer.
func TestMigrateWarnsWhenItRecreatesLostTombstones(t *testing.T) {
	notice := migrateLostTombstonesUpToFirstApply(t, LatestIgnoredVersion())
	if !strings.Contains(notice, "wisp_id_tombstones was missing") || !strings.Contains(notice, "new logical scope") {
		t.Fatalf("migrate re-created a lost wisp_id_tombstones with notice %q; want the loss named", notice)
	}
}

// TestMigrateIsQuietWhenTheTombstoneTableWasNeverCreated pins the other side:
// a cursor at or below the floor never ran ignored/0030, so the table is being
// created for the first time and nothing was lost.
func TestMigrateIsQuietWhenTheTombstoneTableWasNeverCreated(t *testing.T) {
	if notice := migrateLostTombstonesUpToFirstApply(t, 29); notice != "" {
		t.Fatalf("migrate printed %q creating wisp_id_tombstones for the first time; nothing was lost", notice)
	}
}
