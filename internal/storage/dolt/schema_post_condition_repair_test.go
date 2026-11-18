package dolt

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// postConditionRepairCommitMessage is the message schema.RepairRecordedPostConditions
// gives its own commit (GH#5269).
const postConditionRepairCommitMessage = "schema: repair recorded migration post-conditions (GH#5269)"

// columnDefaultThrough reads table.column's default through db, the way any
// later caller of store.DB() would.
func columnDefaultThrough(ctx context.Context, t *testing.T, db *sql.DB, table, column string) sql.NullString {
	t.Helper()
	var def sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, column).Scan(&def); err != nil {
		t.Fatalf("read %s.%s default: %v", table, column, err)
	}
	return def
}

// TestApplySchemaMigrationsAndRepairRepairsRecordedDefaultDrift_5269 is the
// server-mode `bd migrate schema` (GH#5269, AC-3/AC-5): on a converged store
// whose recorded marks claim dropped defaults that are back in place — one on
// a tracked table, one on a dolt-ignored one —
// DoltStore.ApplySchemaMigrationsAndRepair takes the migration lock instead
// of the convergence fast path, drops both defaults, commits only the tracked
// table in the repair's own commit, and rebuilds the main pool, so a read
// through store.DB() sees the repaired schema. A repeat repairs nothing, makes
// no commit and leaves the pool alone.
func TestApplySchemaMigrationsAndRepairRepairsRecordedDefaultDrift_5269(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for _, q := range []string{
		// dependencies: the default 0043/0050 drop, committed under the marks.
		"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
		"CALL DOLT_COMMIT('-Am', 'test: drift dependencies.id under the recorded marks')",
		// wisp_events: the default ignored 0010 drops, on a dolt-ignored table.
		"ALTER TABLE wisp_events ALTER COLUMN id SET DEFAULT (UUID())",
	} {
		if _, err := store.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, tc := range [][2]string{{"dependencies", "id"}, {"wisp_events", "id"}} {
		if def := columnDefaultThrough(ctx, t, store.db, tc[0], tc[1]); !def.Valid {
			t.Fatalf("precondition: %s.%s has no default after the drift", tc[0], tc[1])
		}
	}
	requireCleanTables(ctx, t, store, "dependencies")
	commitsBefore := doltCommitCount(ctx, t, store)
	poolBefore := store.db

	res, err := store.ApplySchemaMigrationsAndRepair(ctx)
	if err != nil {
		t.Fatalf("ApplySchemaMigrationsAndRepair: %v", err)
	}
	if res.Applied != 0 {
		t.Fatalf("applied = %d, want 0 on a converged store", res.Applied)
	}
	if want := []string{"dependencies.id", "wisp_events.id"}; !reflect.DeepEqual(res.Repaired, want) {
		t.Fatalf("repaired = %v, want %v (a converged store must still reach the repair)", res.Repaired, want)
	}
	if len(res.Uncommitted) != 0 {
		t.Fatalf("uncommitted = %v, want none after a repair that committed", res.Uncommitted)
	}
	if store.db == poolBefore {
		t.Fatalf("main pool not rebuilt after the repair; pooled sessions may keep the pre-repair root")
	}
	for _, tc := range [][2]string{{"dependencies", "id"}, {"wisp_events", "id"}} {
		if def := columnDefaultThrough(ctx, t, store.DB(), tc[0], tc[1]); def.Valid {
			t.Fatalf("%s.%s still has DEFAULT %q through store.DB() after the repair", tc[0], tc[1], def.String)
		}
	}

	if got := doltCommitCount(ctx, t, store); got != commitsBefore+1 {
		t.Fatalf("commits = %d after the repair, want %d (exactly the repair's own commit)", got, commitsBefore+1)
	}
	var headMessage string
	if err := store.db.QueryRowContext(ctx,
		"SELECT message FROM dolt_log ORDER BY commit_order DESC LIMIT 1").Scan(&headMessage); err != nil {
		t.Fatalf("read HEAD message: %v", err)
	}
	if headMessage != postConditionRepairCommitMessage {
		t.Fatalf("HEAD message = %q, want the repair's own commit %q", headMessage, postConditionRepairCommitMessage)
	}
	rows, err := store.db.QueryContext(ctx, "SELECT to_table_name FROM dolt_diff_summary('HEAD~', 'HEAD') ORDER BY to_table_name")
	if err != nil {
		t.Fatalf("dolt_diff_summary of the repair commit: %v", err)
	}
	var changed []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan: %v", err)
		}
		changed = append(changed, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	_ = rows.Close()
	if want := []string{"dependencies"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("repair commit changed %v, want only %v (ignored tables are never committed)", changed, want)
	}
	requireCleanTables(ctx, t, store, "dependencies")

	poolAfterRepair := store.db
	res, err = store.ApplySchemaMigrationsAndRepair(ctx)
	if err != nil {
		t.Fatalf("repeat ApplySchemaMigrationsAndRepair: %v", err)
	}
	if res.Applied != 0 || len(res.Repaired) != 0 {
		t.Fatalf("repeat = applied %d, repaired %v; want a no-op", res.Applied, res.Repaired)
	}
	if got := doltCommitCount(ctx, t, store); got != commitsBefore+1 {
		t.Fatalf("repeat moved the commit count to %d, want %d (no new commit)", got, commitsBefore+1)
	}
	if store.db != poolAfterRepair {
		t.Fatalf("a repeat that changed nothing rebuilt the main pool")
	}
}

// TestApplySchemaMigrationsAndRepairDoesNotRepairThroughAGateway_5269: a
// gateway server owns the schema, and bd issues it no schema DDL
// (Config.Gateway). So `bd migrate schema` through a gateway leaves recorded
// default drift alone: no repair, no commit, the default still in place. It
// runs exactly as ApplySchemaMigrations, which on this converged database is
// the convergence fast path. bd doctor names the server administrator's fix
// instead.
func TestApplySchemaMigrationsAndRepairDoesNotRepairThroughAGateway_5269(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for _, q := range []string{
		"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
		"CALL DOLT_COMMIT('-Am', 'test: drift dependencies.id under the recorded marks')",
	} {
		if _, err := store.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	commitsBefore := doltCommitCount(ctx, t, store)

	gateway, err := New(ctx, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       store.cfg.Database,
		MaxOpenConns:   2,
		Gateway:        true,
	})
	if err != nil {
		t.Fatalf("New (gateway): %v", err)
	}
	defer gateway.Close()
	if !gateway.IsGateway() {
		t.Fatalf("IsGateway() = false for a store opened with Config.Gateway")
	}

	res, err := gateway.ApplySchemaMigrationsAndRepair(ctx)
	if err != nil {
		t.Fatalf("ApplySchemaMigrationsAndRepair (gateway): %v", err)
	}
	if res.Applied != 0 || len(res.Repaired) != 0 || len(res.Uncommitted) != 0 {
		t.Fatalf("gateway result = %+v, want nothing applied or repaired", res)
	}
	if def := columnDefaultThrough(ctx, t, store.db, "dependencies", "id"); !def.Valid {
		t.Fatalf("dependencies.id lost its default through a gateway, which owns the schema")
	}
	if got := doltCommitCount(ctx, t, store); got != commitsBefore {
		t.Fatalf("commits = %d after the gateway call, want %d (no repair commit)", got, commitsBefore)
	}
	requireCleanTables(ctx, t, store, "dependencies")
}
