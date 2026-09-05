//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestSchemaInitReplaysMigration0067WhenBookkeepingRowMissing reproduces,
// against a real shared dolt sql-server, the designated-migrator replay
// scenario for migration 0067 (issue_versions, store_epoch,
// issues.current_revision): the schema_migrations bookkeeping row for version
// 67 is missing, but the physical tables and column already exist from the
// original init — the same state stageBehindRemoteBackedDB
// (cmd/bd/protocol/versioning_contract_test.go) forges before
// TestProtocol_V2_DesignatedMigratorOverride runs `bd create` with
// BD_ALLOW_REMOTE_MIGRATE=1.
//
// Before the fix, replaying migration 67's frozen CREATE TABLE issue_versions
// dies with Error 1105 ("table with name issue_versions already exists");
// once that's guarded, CREATE TABLE store_epoch would die the same way, and
// once both are guarded, ALTER TABLE issues ADD COLUMN current_revision would
// die on a duplicate-column error. All four guards now live in the frozen
// file itself: CREATE TABLE IF NOT EXISTS for the tables, and an
// INFORMATION_SCHEMA-probed PREPARE for each plane's ADD COLUMN (MySQL, which
// Dolt follows, has no ADD COLUMN IF NOT EXISTS — that is a MariaDB-only
// extension). Keeping the guard in the SQL rather than in the Go runtime path
// is what makes the file idempotent for callers that execute its bytes
// directly, which is the shape
// TestPR4107Migration0067ReplaysIdempotentlyAsRawSQL exercises.
func TestSchemaInitReplaysMigration0067WhenBookkeepingRowMissing(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	rootDB, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open root connection: %v", err)
	}
	defer rootDB.Close()

	dbName := uniqueTestDBName(t)
	if _, err := rootDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	dbDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	db, err := sql.Open("mysql", dbDSN)
	if err != nil {
		t.Fatalf("open db connection: %v", err)
	}
	defer db.Close()

	// 1. Fully initialize to the latest schema — issue_versions, store_epoch,
	//    and issues.current_revision all physically exist afterward.
	if _, err := initSchemaOnDB(ctx, db); err != nil {
		t.Fatalf("initial initSchemaOnDB: %v", err)
	}
	latest := schema.LatestVersion()
	assertSchemaVersionAtLeast(ctx, t, db, latest)

	// 2. Forge the exact state stageBehindRemoteBackedDB puts a clone in: the
	//    version-67 bookkeeping row is gone, but nothing about the physical
	//    tables/column changed. Commit the delete so it is not left staged —
	//    an uncommitted schema_migrations write would instead trip the
	//    unrelated "pending migration alters a dirty table" guard
	//    (gastownhall/beads#4566) before the replay is ever reached, same as
	//    the 0040/0041 forges above.
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 67"); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'forge missing 0067 bookkeeping row, tables/column already applied')"); err != nil {
		t.Fatalf("commit forged partial state: %v", err)
	}

	// 3. Re-init replays migration 67 against a database that already has its
	//    tables and columns. RED (pre-fix): dies on Error 1105, "table with
	//    name issue_versions already exists" — the same failure
	//    TestProtocol_V2_DesignatedMigratorOverride hits at the full
	//    bd-binary integration level. GREEN (post-fix): CREATE TABLE IF NOT
	//    EXISTS plus the two guarded ADD COLUMNs make the replay a clean
	//    no-op, and the chain reaches latest again.
	if _, err := initSchemaOnDB(ctx, db); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("re-init after forged missing 0067 bookkeeping row failed, but not with the expected Error 1105 \"already exists\" shape — want the same failure TestProtocol_V2_DesignatedMigratorOverride hits: %v", err)
		}
		t.Fatalf("re-init after forged missing 0067 bookkeeping row (this is the designated-migrator replay bug, be-xrl84): %v", err)
	}
	assertSchemaVersionAtLeast(ctx, t, db, latest)
}

// TestMigration0067ReplaysIdempotentlyAsRawSQLThroughPR4107Harness is the
// raw-SQL half of the same guarantee, and the half no Go-side guard can
// provide.
//
// pr4107_corruption_test.go's runMigrationSQLFilesFrom re-executes the frozen
// .up.sql bytes from 0046 onward straight through the driver, against a store
// setupTestStore has already migrated to latest. Nothing in
// internal/storage/schema is in that path — not execMigrationBody, not any
// filename-keyed override — so every migration >= 0046 has to be idempotent
// as raw SQL on its own. 0067's two ADD COLUMNs are guarded on
// INFORMATION_SCHEMA for exactly this, and this test is the proof, run
// through that harness rather than a hand-written double-apply: the same call
// TestPR4107IssueIsBlockedMigrationMatchesRuntimeMixedGraphSemantics makes,
// made twice.
//
// It also pins that the guard NO-OPS rather than re-applies: a column whose
// replay silently dropped and re-added it would pass a mere "the column
// exists" check while resetting every row to the DEFAULT. Phase 1 writes no
// revisions, so the seeded value below is the only way to see that
// difference from here.
func TestMigration0067ReplaysIdempotentlyAsRawSQLThroughPR4107Harness(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "mig0067-replay-subject")
	if _, err := store.db.ExecContext(ctx,
		"UPDATE issues SET current_revision = 42 WHERE id = 'mig0067-replay-subject'"); err != nil {
		t.Fatalf("seed current_revision: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO issue_versions (issue_id, revision, epoch, change_at)
		VALUES ('mig0067-replay-subject', 42, 1, '2026-09-05 00:00:00')`); err != nil {
		t.Fatalf("seed issue_versions row: %v", err)
	}

	for pass := 1; pass <= 2; pass++ {
		runMigrationSQLFilesFrom(t, ctx, store, "../schema/migrations", 46)

		for _, table := range []string{"issues", "wisps"} {
			if got := countColumn(t, ctx, store, table, "current_revision"); got != 1 {
				t.Fatalf("pass %d: %s.current_revision column count = %d, want exactly 1", pass, table, got)
			}
		}

		var revision int64
		if err := store.db.QueryRowContext(ctx,
			"SELECT current_revision FROM issues WHERE id = 'mig0067-replay-subject'").Scan(&revision); err != nil {
			t.Fatalf("pass %d: read back current_revision: %v", pass, err)
		}
		if revision != 42 {
			t.Fatalf("pass %d: issues.current_revision = %d, want the seeded 42 — the replay re-applied the ADD COLUMN instead of no-opping", pass, revision)
		}

		var versionRows int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issue_versions WHERE issue_id = 'mig0067-replay-subject'").Scan(&versionRows); err != nil {
			t.Fatalf("pass %d: count issue_versions: %v", pass, err)
		}
		if versionRows != 1 {
			t.Fatalf("pass %d: issue_versions row count = %d, want 1 — CREATE TABLE IF NOT EXISTS must not recreate the table", pass, versionRows)
		}

		if got := countColumn(t, ctx, store, "store_epoch", "epoch"); got != 1 {
			t.Fatalf("pass %d: store_epoch.epoch column count = %d, want 1", pass, got)
		}
	}
}

// countColumn returns how many INFORMATION_SCHEMA.COLUMNS rows table.column
// has in the current database — 0 or 1 in practice, and the assertion that
// distinguishes "the guard held" from "the replay added a second one".
func countColumn(t *testing.T, ctx context.Context, store *DoltStore, table, column string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&n); err != nil {
		t.Fatalf("count %s.%s: %v", table, column, err)
	}
	return n
}
