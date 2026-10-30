//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestEmbeddedMigration0074CreatesTheTombstoneTables pins migration 0074 on a
// real engine, upgrading a database that sits at 0072:
//
//   - both tables exist with exactly the two columns and the id primary key;
//   - id_tombstones is VERSIONED: a row written to it shows in dolt_status;
//   - wisp_id_tombstones is CLONE-LOCAL: the existing wisp_% ignore pattern
//     keeps a row written to it out of dolt_status;
//   - a replay of the frozen bytes is a no-op that keeps both rows.
//
// The down migration's text is pinned by the schema package's 0074 test.
func TestEmbeddedMigration0074CreatesTheTombstoneTables(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 72)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp from 0072: %v", err)
	}

	for _, table := range []string{"id_tombstones", "wisp_id_tombstones"} {
		requireTombstoneShape(t, ctx, conn, table)
	}

	execFrozenGuard(t, ctx, conn, "INSERT INTO id_tombstones (id, closed_at) VALUES ('mig-durable', '2026-03-01 12:00:00')")
	execFrozenGuard(t, ctx, conn, "INSERT INTO wisp_id_tombstones (id, closed_at) VALUES ('mig-wisp', '2026-03-01 12:00:00')")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'id_tombstones'"); got == 0 {
		t.Error("id_tombstones is not in dolt_status after a write: the durable plane's tombstones must be versioned")
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'wisp_id_tombstones'"); got != 0 {
		t.Error("wisp_id_tombstones is in dolt_status after a write: the wisps plane's tombstones must be clone-local (wisp_% ignore pattern)")
	}

	upSQL, err := schema.MigrationSQL("0074_create_id_tombstones.up.sql")
	if err != nil {
		t.Fatalf("read 0074: %v", err)
	}
	execFrozenGuard(t, ctx, conn, upSQL)
	if got := scalarInt(t, ctx, conn, "SELECT (SELECT COUNT(*) FROM id_tombstones) + (SELECT COUNT(*) FROM wisp_id_tombstones)"); got != 2 {
		t.Fatalf("tombstones after a 0074 replay = %d, want the 2 written before it", got)
	}

}

// TestEmbeddedIgnoredMigration0030RestoresALostTombstoneTable simulates a
// clone that arrived (or lost its clone-local plane) without
// wisp_id_tombstones while the main cursor is at-latest: the sentinel floor
// makes the next MigrateUp replay ignored/0030 and re-create it — EMPTY, which
// is the scope reset migrations/ignored/0030 documents — rather than leaving
// every create failing closed on the missing table.
func TestEmbeddedIgnoredMigration0030RestoresALostTombstoneTable(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	execFrozenGuard(t, ctx, conn, "DROP TABLE wisp_id_tombstones")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp after losing wisp_id_tombstones: %v", err)
	}
	requireTombstoneShape(t, ctx, conn, "wisp_id_tombstones")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM wisp_id_tombstones"); got != 0 {
		t.Fatalf("re-created wisp_id_tombstones holds %d rows, want 0", got)
	}

	createSQL, err := schema.IgnoredMigrationSQL("0030_create_wisp_id_tombstones.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0030: %v", err)
	}
	execFrozenGuard(t, ctx, conn, "INSERT INTO wisp_id_tombstones (id, closed_at) VALUES ('kept', '2026-03-01 12:00:00')")
	execFrozenGuard(t, ctx, conn, createSQL)
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM wisp_id_tombstones"); got != 1 {
		t.Fatalf("wisp_id_tombstones rows after an ignored/0030 replay = %d, want 1 (an existing table is never touched)", got)
	}
}

// requireTombstoneShape asserts the compact two-column shape: id
// VARCHAR(255) NOT NULL as the whole primary key, closed_at DATETIME NOT NULL,
// and nothing else.
func requireTombstoneShape(t *testing.T, ctx context.Context, conn *sql.Conn, table string) {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, table)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()
	type column struct{ name, typ, nullable, key string }
	var got []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.typ, &c.nullable, &c.key); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	want := []column{{"id", "varchar(255)", "NO", "PRI"}, {"closed_at", "datetime", "NO", ""}}
	if len(got) != len(want) {
		t.Fatalf("%s columns = %+v, want exactly %+v", table, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s column %d = %+v, want %+v", table, i, got[i], want[i])
		}
	}
}

// TestEmbeddedChildCounterNeverReissuesASweptChild pins the child id mint
// (GetNextChildIDTx) with the parent's child counter in step: it only
// advances, so after a retaining sweep retires child .1 the next child create
// is handed .2 — never the retired .1 it would be refused. A counter that lags
// the retired child is the IDRetention contract's NeverMintsARetiredChildID
// case: there the tombstone alone keeps the suffix taken.
func TestEmbeddedChildCounterNeverReissuesASweptChild(t *testing.T) {
	requireEmbedded(t)
	te := newTestEnv(t, "cc")
	ctx := t.Context()
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	sweeper, err := te.store.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}
	create := func(parent string) string {
		t.Helper()
		result, err := lifecycle.Create(ctx, issueops.CreateRequest{
			Actor:    "child-counter-test",
			ParentID: parent,
			Issue:    &types.Issue{Title: "child counter " + parent, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		})
		if err != nil {
			t.Fatalf("Create(parent=%q) = %v", parent, err)
		}
		return result.Issue.ID
	}
	parent := create("")
	first := create(parent)
	if first != parent+".1" {
		t.Fatalf("first child = %s, want %s.1", first, parent)
	}
	if _, err := lifecycle.Close(ctx, issueops.CloseRequest{Actor: "child-counter-test", IssueID: first, Reason: "done"}); err != nil {
		t.Fatalf("Close(%s) = %v", first, err)
	}
	if result, err := sweeper.Sweep(ctx, issueops.SweepRequest{Tier: issueops.SweepDurable, IDPattern: first, RetainIDs: true}); err != nil || result.Swept != 1 {
		t.Fatalf("retaining sweep of %s = %+v, %v; want Swept 1", first, result, err)
	}
	if second := create(parent); second != parent+".2" {
		t.Fatalf("child after sweeping .1 = %s, want %s.2", second, parent)
	}
}

// TestEmbeddedImportJSONLDataSkipsRetiredRowsOnATombstoneOnlyDatabase pins the
// auto-import's single-transaction body (ImportJSONLData, ConflictSkip) on the
// database a retaining sweep of every row leaves: no issue rows, so it counts
// as empty and the import runs, while the tombstone remains. The retired row,
// which the file carries twice, is skipped and named once; only the row
// actually written counts as imported.
func TestEmbeddedImportJSONLDataSkipsRetiredRowsOnATombstoneOnlyDatabase(t *testing.T) {
	requireEmbedded(t)
	te := newTestEnv(t, "ij")
	ctx := t.Context()
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	sweeper, err := te.store.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper(): %v", err)
	}
	created, err := lifecycle.Create(ctx, issueops.CreateRequest{
		Actor: "import-jsonl-test",
		Issue: &types.Issue{Title: "retire me", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	})
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	retired := created.Issue.ID
	if _, err := lifecycle.Close(ctx, issueops.CloseRequest{Actor: "import-jsonl-test", IssueID: retired, Reason: "done"}); err != nil {
		t.Fatalf("Close(%s) = %v", retired, err)
	}
	if result, err := sweeper.Sweep(ctx, issueops.SweepRequest{Tier: issueops.SweepDurable, IDPattern: retired, RetainIDs: true}); err != nil || result.Swept != 1 {
		t.Fatalf("retaining sweep of %s = %+v, %v; want Swept 1", retired, result, err)
	}

	row := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "imported " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	const fresh = "ij-fresh1"
	imported, retiredIDs, err := te.store.ImportJSONLData(ctx, []*types.Issue{row(retired), row(fresh), row(retired)}, nil, "auto-import")
	if err != nil {
		t.Fatalf("ImportJSONLData = %v, want nil: an import skips a retired row rather than failing", err)
	}
	if imported != 1 {
		t.Errorf("ImportJSONLData imported = %d, want 1 (the fresh row only; neither row of the retired id)", imported)
	}
	if len(retiredIDs) != 1 || retiredIDs[0] != retired {
		t.Errorf("ImportJSONLData retired ids = %v, want [%s]", retiredIDs, retired)
	}
	te.assertRowNotExists(t, ctx, "issues", retired)
	te.assertRowExists(t, ctx, "issues", fresh)
}
