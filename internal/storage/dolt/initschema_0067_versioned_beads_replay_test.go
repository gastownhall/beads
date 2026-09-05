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
// die on a duplicate-column error. Editing the shipped, content-hashed 0067
// SQL bytes is constrained the same way 0040/0041 were (see
// TestSchemaInitRecoversFromPartialNonlocalMigration above): CREATE TABLE IF
// NOT EXISTS is safe, ordinary MySQL-flavored DDL, but MySQL (which Dolt
// follows) has no ADD COLUMN IF NOT EXISTS — that is a MariaDB-only extension
// — so the column guard must come from elsewhere in the runtime migration
// path, not from the frozen file text itself.
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
	//    tables and column. RED (pre-fix): dies on Error 1105, "table with
	//    name issue_versions already exists" — the same failure
	//    TestProtocol_V2_DesignatedMigratorOverride hits at the full
	//    bd-binary integration level. GREEN (post-fix): CREATE TABLE IF NOT
	//    EXISTS plus the ADD COLUMN idempotency guard make the replay a clean
	//    no-op, and the chain reaches latest again.
	if _, err := initSchemaOnDB(ctx, db); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("re-init after forged missing 0067 bookkeeping row failed, but not with the expected Error 1105 \"already exists\" shape — want the same failure TestProtocol_V2_DesignatedMigratorOverride hits: %v", err)
		}
		t.Fatalf("re-init after forged missing 0067 bookkeeping row (this is the designated-migrator replay bug, be-xrl84): %v", err)
	}
	assertSchemaVersionAtLeast(ctx, t, db, latest)
}
