package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestMigration0073RollbackAndReapplyRebuildsTheArrivalSeqIndex pins a manual
// rollback of 0073 and the migration pass after it. The down file drops
// wisps.arrival_seq, and uk_wisps_arrival_seq with it, but leaves ignored/0029,
// the twin that also builds the index, recorded, so of the two files that
// build it the pass that applies 0073 again runs 0073 alone. Without the index
// a counter left behind its stamps hands out a duplicate instead of colliding
// and healing.
//
// The down runs as raw SQL, as an operator runs it, with its cursor row deleted
// beside it, after the migrations above 0073 are rolled back the same way
// (rollBackMainMigrationsAbove); the re-apply is the real migration pass.
func TestMigration0073RollbackAndReapplyRebuildsTheArrivalSeqIndex(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const downFile = "../schema/migrations/0073_add_arrival_seq.down.sql"

	store.SetEventsJournalEnabled(true)
	createWisp := func(id string) {
		t.Helper()
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
		}, "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	createWisp("rb73-a")
	createWisp("rb73-b")
	stampedBefore := queryInt64(ctx, t, store, "SELECT COALESCE(MAX(arrival_seq), 0) FROM wisps")
	if stampedBefore == 0 {
		t.Fatal("precondition failed: the wisps created while journaling carry no stamp")
	}
	if !arrivalSeqUniqueIndex(ctx, t, store) {
		t.Fatal("precondition failed: a migrated store has no uk_wisps_arrival_seq")
	}

	rollBackMainMigrationsAbove(t, ctx, store, 73)
	runMigrationSQL(t, ctx, store, downFile)
	if _, err := store.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 73"); err != nil {
		t.Fatalf("delete the 0073 cursor row: %v", err)
	}
	// Commit the rollback, as `bd dolt commit` would: the next pass refuses to
	// migrate over a dirty issues table (#4566).
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'roll back migration 0073')"); err != nil {
		t.Fatalf("commit the rollback: %v", err)
	}
	for _, table := range []string{"issues", "wisps"} {
		if hasArrivalSeqColumn(ctx, t, store, table) {
			t.Fatalf("%s.arrival_seq survived 0073's down", table)
		}
	}
	if arrivalSeqUniqueIndex(ctx, t, store) {
		t.Fatal("uk_wisps_arrival_seq survived 0073's down")
	}

	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("migration pass after the rollback: %v", err)
	}
	if got := queryInt64(ctx, t, store, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != int64(schema.LatestVersion()) {
		t.Fatalf("schema cursor = %d after the pass, want %d", got, schema.LatestVersion())
	}
	// ignored/0029 stayed recorded and did not run again: whatever rebuilt the
	// index below was 0073.
	if got := queryInt64(ctx, t, store, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != int64(schema.LatestIgnoredVersion()) {
		t.Fatalf("ignored cursor = %d, want it still at %d", got, schema.LatestIgnoredVersion())
	}
	for _, table := range []string{"issues", "wisps"} {
		if !hasArrivalSeqColumn(ctx, t, store, table) {
			t.Fatalf("%s.arrival_seq is missing after 0073 was applied again", table)
		}
	}
	if !arrivalSeqUniqueIndex(ctx, t, store) {
		t.Fatal("0073 applied again left wisps.arrival_seq without uk_wisps_arrival_seq: a counter behind its stamps would now mint duplicates")
	}

	// The down took every stamp with the column and never touched the counter,
	// so the counter still sits at or past each stamp handed out before the
	// rollback, and the next create stamps above all of them.
	if next := queryInt64(ctx, t, store, "SELECT next_seq FROM bd_events_seq WHERE id = 0"); next < stampedBefore {
		t.Fatalf("next_seq = %d, below the stamp %d handed out before the rollback", next, stampedBefore)
	}
	createWisp("rb73-c")
	stamp := queryInt64(ctx, t, store, "SELECT COALESCE(arrival_seq, 0) FROM wisps WHERE id = 'rb73-c'")
	if stamp <= stampedBefore {
		t.Fatalf("the first wisp after the rollback stamped %d, want above %d", stamp, stampedBefore)
	}
	// The rebuilt index is live: no second row can take that stamp.
	if _, err := store.db.ExecContext(ctx, "UPDATE wisps SET arrival_seq = ? WHERE id = 'rb73-a'", stamp); err == nil {
		t.Fatalf("a second wisp took arrival_seq %d: uk_wisps_arrival_seq is not enforcing", stamp)
	}
}

// rollBackMainMigrationsAbove rolls a migrated store back to version the way
// an operator does by hand: newest first, it runs the down file of every main
// migration above version and deletes that migration's cursor row beside it.
// A later row left in schema_migrations would hold the cursor past version,
// and the next pass would never apply the migrations at and below it again.
func rollBackMainMigrationsAbove(t *testing.T, ctx context.Context, store *DoltStore, version int) {
	t.Helper()
	for v := schema.LatestVersion(); v > version; v-- {
		downs, err := filepath.Glob(fmt.Sprintf("../schema/migrations/%04d_*.down.sql", v))
		if err != nil || len(downs) != 1 {
			t.Fatalf("down migration of %04d = %v (glob error %v), want exactly one file", v, downs, err)
		}
		runMigrationSQL(t, ctx, store, downs[0])
		if _, err := store.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", v); err != nil {
			t.Fatalf("delete the %04d cursor row: %v", v, err)
		}
	}
}

func queryInt64(ctx context.Context, t *testing.T, store *DoltStore, query string) int64 {
	t.Helper()
	var v int64
	if err := store.db.QueryRowContext(ctx, query).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

func hasArrivalSeqColumn(ctx context.Context, t *testing.T, store *DoltStore, table string) bool {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SHOW COLUMNS FROM "+table+" LIKE 'arrival_seq'")
	if err != nil {
		t.Fatalf("SHOW COLUMNS FROM %s: %v", table, err)
	}
	defer rows.Close()
	found := rows.Next()
	if err := rows.Err(); err != nil {
		t.Fatalf("SHOW COLUMNS FROM %s: %v", table, err)
	}
	return found
}

// arrivalSeqUniqueIndex reports whether wisps carries uk_wisps_arrival_seq as a
// unique index over arrival_seq.
func arrivalSeqUniqueIndex(ctx context.Context, t *testing.T, store *DoltStore) bool {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SHOW INDEX FROM wisps")
	if err != nil {
		t.Fatalf("SHOW INDEX FROM wisps: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("SHOW INDEX columns: %v", err)
	}
	cells := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range cells {
		dest[i] = &cells[i]
	}
	found := false
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan SHOW INDEX: %v", err)
		}
		row := map[string]string{}
		for i, col := range cols {
			row[strings.ToLower(col)] = string(cells[i])
		}
		if row["key_name"] == "uk_wisps_arrival_seq" && row["column_name"] == "arrival_seq" && row["non_unique"] == "0" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("SHOW INDEX FROM wisps: %v", err)
	}
	return found
}
