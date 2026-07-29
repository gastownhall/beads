//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrateRepairedDependenciesIDColumnCommitsAtomicallyWithVersion53_4690
// is the atomicity regression for the migration-chain-hardening branch's
// finding #6: preMigrationRepair (registered for main-source version 53,
// see internal/storage/schema/migration_repairs.go's ensureDependenciesIDColumn,
// #4690) can itself mutate a synced table -- ALTER TABLE dependencies ADD
// COLUMN id / MODIFY ... NOT NULL / ADD PRIMARY KEY. Before the fix,
// runMigrations snapshotted the "before" dirty-table state AFTER the repair
// ran, so the repair's own mutation to `dependencies` was misclassified as
// pre-existing dirt to exclude from version 53's per-step commit: the cursor
// row for v53 would land in one Dolt commit while the repaired
// `dependencies` table sat uncommitted in the working set, picked up (if at
// all) only by MigrateUp's much-later end-of-pass catch-all commit. A
// process killed anywhere between the v53 step-commit and that later commit
// would leave committed history claiming v53 applied while
// dependencies.id was never durably recorded -- and the repair cannot
// re-run to fix it, because its version is no longer pending once the
// cursor row is committed.
//
// The fix (schema.go's runMigrations) snapshots dirty tables BEFORE calling
// preMigrationRepair, not after, so the repair's mutations are attributed to
// version 53's own step and land in the SAME atomic commit as its cursor
// row. This test proves that directly: fault-inject a "kill" immediately
// after version 53's step-commit (the same seam #4566's self-heal tests use)
// and read dependencies back AS OF HEAD -- the commit the fault fires right
// after -- to prove the repaired column is already durably there, not
// stranded in a working set the injected kill would otherwise lose.
func TestEmbeddedMigrateRepairedDependenciesIDColumnCommitsAtomicallyWithVersion53_4690(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 52)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	// Reproduce #4690: dependencies already in split-target/content-derived-
	// key shape (0043 has run, so the uk_dep_* unique keys are present) but
	// its surrogate id column and primary key are gone -- a different
	// historical migration path than this repo's 0043, which adds them.
	if _, err := conn.ExecContext(ctx, "ALTER TABLE dependencies DROP PRIMARY KEY"); err != nil {
		closeConn()
		t.Fatalf("drop dependencies primary key: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "ALTER TABLE dependencies DROP COLUMN id"); err != nil {
		closeConn()
		t.Fatalf("drop dependencies.id: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		closeConn()
		t.Fatalf("stage dependencies drop: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'test: reproduce #4690 missing dependencies.id')"); err != nil {
		closeConn()
		t.Fatalf("commit dependencies drop: %v", err)
	}
	closeConn()

	const killAfter = 53
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == killAfter {
			return fmt.Errorf("injected fault: process killed after migration %d", killAfter)
		}
		return nil
	})
	defer restore()

	conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
	_, err := schema.MigrateUp(ctx, conn2)
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		closeConn2()
		t.Fatalf("MigrateUp err = %v, want the injected fault after migration %d", err, killAfter)
	}

	// The repair's dependencies.id ALTER/MODIFY/ADD PRIMARY KEY ran as part
	// of version 53's pre-migration repair. Prove it committed ATOMICALLY
	// with the v53 cursor row -- not left dangling in the (possibly still
	// dirty) working set for a commit that never happens, since the process
	// was "killed" right here -- by reading it back AS OF the HEAD commit.
	rows, err := conn2.QueryContext(ctx, "SELECT * FROM dependencies AS OF 'HEAD' LIMIT 0")
	if err != nil {
		closeConn2()
		t.Fatalf("query dependencies AS OF HEAD: %v", err)
	}
	cols, err := rows.Columns()
	rows.Close()
	if err != nil {
		closeConn2()
		t.Fatalf("read dependencies columns AS OF HEAD: %v", err)
	}
	hasID := false
	for _, c := range cols {
		if c == "id" {
			hasID = true
		}
	}
	if !hasID {
		closeConn2()
		t.Fatal("dependencies.id missing AS OF HEAD after the v53 step-commit and injected fault: " +
			"the repair mutation was not committed atomically with the v53 cursor row (the #6 atomicity gap)")
	}

	var committedVersion int
	if err := conn2.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version),0) FROM schema_migrations AS OF 'HEAD'").Scan(&committedVersion); err != nil {
		closeConn2()
		t.Fatalf("read committed schema version: %v", err)
	}
	if committedVersion != killAfter {
		closeConn2()
		t.Fatalf("committed schema version AS OF HEAD = %d, want %d (the step the fault fires right after)",
			committedVersion, killAfter)
	}

	// Close this connection and restore the fault hook BEFORE opening the
	// retry connection: MigrateUp's advisory lock is scoped to the
	// connection that acquired it, and leaving conn2 open while conn3
	// attempts the same lock deadlocks the retry (matches the established
	// #4566 self-heal pattern in migrate_selfheal_test.go).
	closeConn2()
	restore()

	// A plain retry (the #4566 supervisor contract) converges with no manual
	// commit, ending fully backfilled, keyed, and clean.
	conn3, closeConn3 := openPinnedConn(t, ctx, dataDir)
	defer closeConn3()
	if _, err := schema.MigrateUp(ctx, conn3); err != nil {
		t.Fatalf("retry MigrateUp: %v", err)
	}
	if v := currentMainVersion(t, ctx, conn3); v != schema.LatestVersion() {
		t.Fatalf("schema version after retry = %d, want latest %d", v, schema.LatestVersion())
	}
	if dirty := dirtyTableNames(t, ctx, conn3); len(dirty) != 0 {
		t.Fatalf("working set after retry is dirty: %v, want clean", dirty)
	}
	var stillNull int
	if err := conn3.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies WHERE id IS NULL").Scan(&stillNull); err != nil {
		t.Fatalf("count NULL dependencies.id after retry: %v", err)
	}
	if stillNull != 0 {
		t.Fatalf("dependencies rows with NULL id after retry = %d, want 0", stillNull)
	}
}
