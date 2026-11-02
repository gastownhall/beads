//go:build cgo

package embeddeddolt_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// residualContentColumn is one of the five columns migration 0075 widens
// (GH#3925), with the type it must end at and the nullability it was
// created with.
type residualContentColumn struct {
	table, column, wantType, wantNullable string
}

var residualContentColumns = []residualContentColumn{
	{"events", "comment", "longtext", "YES"},
	{"wisp_events", "comment", "longtext", "YES"},
	{"issue_snapshots", "original_content", "longtext", "NO"},
	{"issue_snapshots", "archived_events", "longtext", "YES"},
	{"compaction_snapshots", "snapshot_json", "longblob", "NO"},
}

// columnDefinition is what MODIFY COLUMN could change besides the type.
type columnDefinition struct {
	columnType, nullable string
	columnDefault        sql.NullString
}

func readColumnDefinition(t *testing.T, ctx context.Context, conn *sql.Conn, table, column string) columnDefinition {
	t.Helper()
	var d columnDefinition
	if err := conn.QueryRowContext(ctx, `
SELECT COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, column).Scan(&d.columnType, &d.nullable, &d.columnDefault); err != nil {
		t.Fatalf("read %s.%s definition: %v", table, column, err)
	}
	return d
}

func requireColumnType(t *testing.T, ctx context.Context, conn *sql.Conn, table, column, want string) {
	t.Helper()
	if got := readColumnDefinition(t, ctx, conn, table, column).columnType; got != want {
		t.Fatalf("%s.%s COLUMN_TYPE = %s, want %s", table, column, got, want)
	}
}

func requireTableCount(t *testing.T, ctx context.Context, conn *sql.Conn, table string, want int) {
	t.Helper()
	got := scalarInt(t, ctx, conn,
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table)
	if got != want {
		t.Fatalf("%s table presence count = %d, want %d", table, got, want)
	}
}

// residualText returns n bytes of non-repeating ASCII ending in a marker, so
// a truncated round-trip cannot compare equal by accident.
func residualText(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%07d ", i)
	}
	return b.String()[:n-len("<END>")] + "<END>"
}

// residualBinary returns n bytes cycling through every byte value, NUL and
// invalid UTF-8 included, for the binary snapshot column.
func residualBinary(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// residualFixture inserts one parent issue and one parent wisp, plus a row in
// each affected table carrying value in the column under test.
type residualFixture struct {
	t    *testing.T
	ctx  context.Context
	conn *sql.Conn
	n    int
}

func (f *residualFixture) parents() {
	f.t.Helper()
	execFrozenGuard(f.t, f.ctx, f.conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('mig-issue', 'i', '', '', '', '', 'open', 2, 'task');
INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES ('mig-wisp', 'w', '', '', '', '', 'open', 2, 'task', 1);
`)
}

func (f *residualFixture) nextID(prefix string) string {
	f.n++
	return fmt.Sprintf("%s-%d", prefix, f.n)
}

// insert writes value into c's column and returns the new row's id.
func (f *residualFixture) insert(c residualContentColumn, value any) (string, error) {
	switch c.table {
	case "events":
		id := f.nextID("evt")
		_, err := f.conn.ExecContext(f.ctx,
			"INSERT INTO events (id, issue_id, event_type, actor, comment, created_at) VALUES (?, 'mig-issue', 'commented', 'tester', ?, '2026-03-01 12:00:00')", id, value)
		return id, err
	case "wisp_events":
		id := f.nextID("wevt")
		_, err := f.conn.ExecContext(f.ctx,
			"INSERT INTO wisp_events (id, issue_id, event_type, actor, comment) VALUES (?, 'mig-wisp', 'commented', 'tester', ?)", id, value)
		return id, err
	case "issue_snapshots":
		id := f.nextID("snap")
		original, archived := any("small original"), any(nil)
		if c.column == "original_content" {
			original = value
		} else {
			archived = value
		}
		_, err := f.conn.ExecContext(f.ctx,
			"INSERT INTO issue_snapshots (id, issue_id, snapshot_time, compaction_level, original_size, compressed_size, original_content, archived_events) VALUES (?, 'mig-issue', '2026-03-01 12:00:00', 1, 0, 0, ?, ?)",
			id, original, archived)
		return id, err
	case "compaction_snapshots":
		id := f.nextID("csnap")
		_, err := f.conn.ExecContext(f.ctx,
			"INSERT INTO compaction_snapshots (id, issue_id, compaction_level, snapshot_json) VALUES (?, 'mig-issue', 1, ?)", id, value)
		return id, err
	}
	f.t.Fatalf("no fixture for %s", c.table)
	return "", nil
}

func (f *residualFixture) mustInsert(c residualContentColumn, value any) string {
	f.t.Helper()
	id, err := f.insert(c, value)
	if err != nil {
		f.t.Fatalf("insert into %s.%s: %.300v", c.table, c.column, err)
	}
	return id
}

// read returns the stored bytes of c's column for row id, and whether it is
// NULL.
func (f *residualFixture) read(c residualContentColumn, id string) ([]byte, bool) {
	f.t.Helper()
	var got []byte
	//nolint:gosec // G201: table and column are test-local literals.
	if err := f.conn.QueryRowContext(f.ctx, "SELECT "+c.column+" FROM "+c.table+" WHERE id = ?", id).Scan(&got); err != nil {
		f.t.Fatalf("read %s.%s row %s: %v", c.table, c.column, id, err)
	}
	return got, got == nil
}

// TestEmbeddedMigration0075WidensResidualContentColumns upgrades a database
// sitting at main version 74, with rows already in the four affected tables,
// through MigrateUp, and pins migration 0075 (+ ignored/0031) on a real engine:
//
//   - before the upgrade every one of the five columns refuses a value past
//     the 65,535-byte TEXT/BLOB cap;
//   - after it each has its widened type and the nullability and default it
//     was created with (MODIFY COLUMN replaces the whole definition);
//   - the existing rows, NULL and the omitted-comment default included, are
//     unchanged;
//   - values at, just past and well past the cap, multibyte text and
//     arbitrary binary bytes round-trip exactly;
//   - replaying both frozen texts on the same database leaves every table
//     definition and row unchanged.
func TestEmbeddedMigration0075WidensResidualContentColumns(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	mainSQL, err := schema.MigrationSQL("0075_widen_residual_content_columns.up.sql")
	if err != nil {
		t.Fatalf("read 0075: %v", err)
	}
	ignoredSQL, err := schema.IgnoredMigrationSQL("0031_widen_event_comment_columns.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0031: %v", err)
	}

	dataDir := seedMainSchemaAt(t, ctx, 74)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	f := &residualFixture{t: t, ctx: ctx, conn: conn}
	f.parents()

	before := map[residualContentColumn]columnDefinition{}
	for _, c := range residualContentColumns {
		before[c] = readColumnDefinition(t, ctx, conn, c.table, c.column)
		if _, err := f.insert(c, residualText(70000)); err == nil {
			t.Fatalf("a 70000-byte %s.%s insert succeeded at version 74 (%s); want the 64 KiB cap to refuse it", c.table, c.column, before[c].columnType)
		}
	}

	// Existing rows: a NULL and a small events.comment, an omitted
	// wisp_events.comment (its '' default), a snapshot with NULL
	// archived_events, and binary snapshot bytes.
	eventsComment, wispComment := residualContentColumns[0], residualContentColumns[1]
	original, archived, snapshotJSON := residualContentColumns[2], residualContentColumns[3], residualContentColumns[4]
	nullEvent := f.mustInsert(eventsComment, nil)
	smallEvent := f.mustInsert(eventsComment, "small reason")
	execFrozenGuard(t, ctx, conn,
		"INSERT INTO wisp_events (id, issue_id, event_type, actor) VALUES ('wevt-omitted', 'mig-wisp', 'commented', 'tester')")
	smallSnapshot := f.mustInsert(original, "small original")
	smallBinary := []byte{0x00, 0xff, '{', 0x80, '}', 0x00}
	smallCompaction := f.mustInsert(snapshotJSON, smallBinary)
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'test: rows before 0075')"); err != nil {
		t.Fatalf("commit pre-upgrade rows: %v", err)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp from 74: %v", err)
	}

	for _, c := range residualContentColumns {
		got := readColumnDefinition(t, ctx, conn, c.table, c.column)
		if got.columnType != c.wantType {
			t.Errorf("%s.%s COLUMN_TYPE = %s, want %s", c.table, c.column, got.columnType, c.wantType)
		}
		if got.nullable != c.wantNullable || got.nullable != before[c].nullable {
			t.Errorf("%s.%s IS_NULLABLE = %s, want %s as created", c.table, c.column, got.nullable, c.wantNullable)
		}
		if got.columnDefault != before[c].columnDefault {
			t.Errorf("%s.%s COLUMN_DEFAULT = %+v, want %+v as created", c.table, c.column, got.columnDefault, before[c].columnDefault)
		}
	}
	if d := readColumnDefinition(t, ctx, conn, "events", "comment").columnDefault; d.Valid {
		t.Errorf("events.comment default = %q, want none (NULL)", d.String)
	}
	if d := readColumnDefinition(t, ctx, conn, "wisp_events", "comment").columnDefault; !d.Valid {
		t.Error("wisp_events.comment lost its '' default")
	}

	requireStored := func(c residualContentColumn, id string, want []byte, wantNull bool) {
		t.Helper()
		got, isNull := f.read(c, id)
		if isNull != wantNull || !bytes.Equal(got, want) {
			t.Errorf("%s.%s row %s = %d bytes (NULL=%v), want %d bytes (NULL=%v) unchanged", c.table, c.column, id, len(got), isNull, len(want), wantNull)
		}
	}
	requireStored(eventsComment, nullEvent, nil, true)
	requireStored(eventsComment, smallEvent, []byte("small reason"), false)
	requireStored(wispComment, "wevt-omitted", []byte{}, false)
	requireStored(original, smallSnapshot, []byte("small original"), false)
	requireStored(archived, smallSnapshot, nil, true)
	requireStored(snapshotJSON, smallCompaction, smallBinary, false)

	for _, c := range residualContentColumns {
		values := [][]byte{
			[]byte(residualText(65535)),
			[]byte(residualText(65536)),
			[]byte(residualText(70000)),
			// 21,846 three-byte euro signs: 65,538 bytes in 21,846 characters.
			[]byte(strings.Repeat("€", 21846)),
		}
		if c.table == "compaction_snapshots" {
			values = append(values, residualBinary(70000))
		}
		for _, v := range values {
			var arg any = string(v)
			if c.table == "compaction_snapshots" {
				arg = v
			}
			requireStored(c, f.mustInsert(c, arg), v, false)
		}
	}

	tables := []string{"events", "wisp_events", "issue_snapshots", "compaction_snapshots"}
	definitions := map[string]string{}
	rowCounts := map[string]int{}
	for _, table := range tables {
		definitions[table] = showCreateTable(t, ctx, conn, table)
		//nolint:gosec // G202: table is a test-local literal.
		rowCounts[table] = scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM "+table)
	}
	execFrozenGuard(t, ctx, conn, mainSQL)
	execFrozenGuard(t, ctx, conn, ignoredSQL)
	for _, table := range tables {
		if got := showCreateTable(t, ctx, conn, table); got != definitions[table] {
			t.Errorf("%s definition changed on a 0075/ignored-0031 replay:\nbefore: %s\nafter:  %s", table, definitions[table], got)
		}
		//nolint:gosec // G202: table is a test-local literal.
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM "+table); got != rowCounts[table] {
			t.Errorf("%s rows after a replay = %d, want %d", table, got, rowCounts[table])
		}
	}
	requireStored(eventsComment, nullEvent, nil, true)
	requireStored(snapshotJSON, smallCompaction, smallBinary, false)
}

// TestEmbeddedMigration0075ConvergesFromPartialWidening runs the frozen 0075
// text on databases that widened unevenly or lack some of its tables. Each
// column has its own guard, so a column already wide stays as it is, a narrow
// one still widens, existing bytes survive, and a missing table stays missing.
func TestEmbeddedMigration0075ConvergesFromPartialWidening(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	mainSQL, err := schema.MigrationSQL("0075_widen_residual_content_columns.up.sql")
	if err != nil {
		t.Fatalf("read 0075: %v", err)
	}
	ignoredSQL, err := schema.IgnoredMigrationSQL("0031_widen_event_comment_columns.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0031: %v", err)
	}
	open := func(t *testing.T) (*sql.Conn, *residualFixture) {
		dataDir := seedMainSchemaAt(t, ctx, 74)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		t.Cleanup(closeConn)
		f := &residualFixture{t: t, ctx: ctx, conn: conn}
		f.parents()
		return conn, f
	}

	for _, tc := range []struct {
		name, alreadyWide, preWiden string
	}{
		{"original_content already wide", "original_content", "ALTER TABLE issue_snapshots MODIFY COLUMN original_content LONGTEXT NOT NULL"},
		{"archived_events already wide", "archived_events", "ALTER TABLE issue_snapshots MODIFY COLUMN archived_events LONGTEXT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, f := open(t)
			execFrozenGuard(t, ctx, conn,
				"INSERT INTO issue_snapshots (id, issue_id, snapshot_time, compaction_level, original_size, compressed_size, original_content, archived_events) VALUES ('snap-kept', 'mig-issue', '2026-03-01 12:00:00', 1, 0, 0, 'kept original', 'kept events')")
			execFrozenGuard(t, ctx, conn, tc.preWiden)
			requireColumnType(t, ctx, conn, "issue_snapshots", tc.alreadyWide, "longtext")

			execFrozenGuard(t, ctx, conn, mainSQL)
			for _, c := range residualContentColumns {
				requireColumnType(t, ctx, conn, c.table, c.column, c.wantType)
			}
			if got := readColumnDefinition(t, ctx, conn, "issue_snapshots", "original_content").nullable; got != "NO" {
				t.Errorf("issue_snapshots.original_content IS_NULLABLE = %s, want NO", got)
			}
			if got := readColumnDefinition(t, ctx, conn, "issue_snapshots", "archived_events").nullable; got != "YES" {
				t.Errorf("issue_snapshots.archived_events IS_NULLABLE = %s, want YES", got)
			}
			var gotOriginal, gotArchived string
			if err := conn.QueryRowContext(ctx, "SELECT original_content, archived_events FROM issue_snapshots WHERE id = 'snap-kept'").Scan(&gotOriginal, &gotArchived); err != nil {
				t.Fatalf("read kept snapshot: %v", err)
			}
			if gotOriginal != "kept original" || gotArchived != "kept events" {
				t.Errorf("kept snapshot = (%q, %q), want (%q, %q)", gotOriginal, gotArchived, "kept original", "kept events")
			}
			big := residualText(70000)
			for _, c := range residualContentColumns[2:4] {
				id := f.mustInsert(c, big)
				if got, _ := f.read(c, id); string(got) != big {
					t.Errorf("%s.%s holds %d bytes, want the %d-byte value verbatim", c.table, c.column, len(got), len(big))
				}
			}
		})
	}

	t.Run("event tables absent", func(t *testing.T) {
		conn, _ := open(t)
		execFrozenGuard(t, ctx, conn, "DROP TABLE events; DROP TABLE wisp_events;")
		execFrozenGuard(t, ctx, conn, mainSQL)
		execFrozenGuard(t, ctx, conn, ignoredSQL)
		requireTableCount(t, ctx, conn, "events", 0)
		requireTableCount(t, ctx, conn, "wisp_events", 0)
		for _, c := range residualContentColumns[2:] {
			requireColumnType(t, ctx, conn, c.table, c.column, c.wantType)
		}
	})

	t.Run("snapshot tables absent", func(t *testing.T) {
		conn, _ := open(t)
		execFrozenGuard(t, ctx, conn, "DROP TABLE issue_snapshots; DROP TABLE compaction_snapshots;")
		execFrozenGuard(t, ctx, conn, mainSQL)
		requireTableCount(t, ctx, conn, "issue_snapshots", 0)
		requireTableCount(t, ctx, conn, "compaction_snapshots", 0)
		for _, c := range residualContentColumns[:2] {
			requireColumnType(t, ctx, conn, c.table, c.column, c.wantType)
		}
	})
}
