//go:build cgo

package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// The unversioned half of the #6089 adoption: bd init against a server
// database in the shape the reporter hit — installed by the v1.0.0-era
// seeder (a committed config table and its legacy schema_version row, no
// schema_migrations table) and then left dirty by a legacy SDK run whose
// EnsureRigIssuePrefix wrote an issue_prefix config row and created the
// routes and interactions tables without ever committing. A modern binary
// opens that database, the dirty-table guard lists exactly those tables,
// and init used to deadlock the workspace. After the #6089 fix it adopts
// the database: one labeled commit of the pre-existing working set, then
// the migrations, and the workspace works end to end.

// legacyConfigDDL is the v1.0.0 seeder's config table — byte-identical to
// migration 0006, which is why the pending migration set touches it.
const legacyConfigDDL = "CREATE TABLE IF NOT EXISTS config (\n" +
	"    `key` VARCHAR(255) PRIMARY KEY,\n" +
	"    value TEXT NOT NULL\n" +
	")"

const legacyRoutesDDL = `CREATE TABLE IF NOT EXISTS routes (
    prefix VARCHAR(32) PRIMARY KEY,
    path VARCHAR(512) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
)`

const legacyInteractionsDDL = `CREATE TABLE IF NOT EXISTS interactions (
    id VARCHAR(32) PRIMARY KEY,
    kind VARCHAR(64) NOT NULL,
    created_at DATETIME NOT NULL,
    actor VARCHAR(255),
    issue_id VARCHAR(255),
    model VARCHAR(255),
    prompt TEXT,
    response TEXT,
    error TEXT,
    tool_name VARCHAR(255),
    exit_code INT,
    parent_id VARCHAR(32),
    label VARCHAR(64),
    reason TEXT,
    extra JSON,
    INDEX idx_interactions_kind (kind),
    INDEX idx_interactions_created_at (created_at),
    INDEX idx_interactions_issue_id (issue_id),
    INDEX idx_interactions_parent_id (parent_id)
)`

// initServerTestUnversionedLegacyResidue builds the fixture: a committed
// v1.0.0-era baseline, then the legacy SDK's uncommitted working set.
func initServerTestUnversionedLegacyResidue(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	// The committed baseline a v1.0.0-era install left behind: the config
	// table with its version marker, and no schema_migrations table — the
	// seeder predates the migration framework.
	exec(legacyConfigDDL)
	exec("INSERT INTO config (`key`, value) VALUES ('schema_version', '11')")
	initServerTestCommit(t, db, "test: v1.0.0-era seeder baseline")

	// The legacy SDK's uncommitted residue: an issue_prefix write plus the
	// routes and interactions tables it created, never committed.
	exec("INSERT INTO config (`key`, value) VALUES ('issue_prefix', 'hq')")
	exec(legacyRoutesDDL)
	exec(legacyInteractionsDDL)
	exec("INSERT INTO interactions (id, kind, created_at, actor, issue_id) VALUES ('ia-1', 'note', NOW(), 'gt', 'bd-legacy')")
}

// TestCLI_InitServerUnversionedDirtyWorkingSet_E2E pins the #6089 adoption:
// bd init against the unversioned dirty legacy shape commits the
// pre-existing working set in its own labeled commit, applies the pending
// migrations, and leaves a fully working workspace — without any
// migration-consent or shared-store unlock in the environment, exactly
// like the reporter's `gt install` (a cursor of 0 passes both gates).
func TestCLI_InitServerUnversionedDirtyWorkingSet_E2E(t *testing.T) {
	skipIfNoDolt(t)
	env := externalServerTestEnv(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	db := createInitServerTestDatabase(t, database)
	initServerTestUnversionedLegacyResidue(t, db)

	// Preconditions: exactly the #6089 shape.
	if got := initServerTestMainCursor(t, db); got != 0 {
		t.Fatalf("main cursor = %d, want 0 (no schema_migrations table)", got)
	}
	dirty := initServerTestDirtyTables(t, db)
	for _, table := range []string{"config", "routes", "interactions"} {
		if !dirty[table] {
			t.Fatalf("dolt_status = %v, want %s dirty before init", dirty, table)
		}
	}

	stdout, stderr, err := runInitServerBD(t, repoDir, env, initServerTestArgs(database)...)
	if err != nil {
		t.Fatalf("bd init on an unversioned dirty database failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	// The migrations ran, and the adopted working set is clean.
	if got, want := initServerTestMainCursor(t, db), schema.LatestVersion(); got != want {
		t.Fatalf("main cursor = %d, want %d (the pending migrations must have run)", got, want)
	}
	if dirty := initServerTestDirtyTables(t, db); len(dirty) != 0 {
		t.Fatalf("dolt_status = %v, want a clean working set after init", dirty)
	}

	// The adopted database is a working bd workspace end to end.
	_, stderr, err = runInitServerBD(t, repoDir, env, "create", "--json", "Adopted workspace issue")
	if err != nil {
		t.Fatalf("bd create after adoption failed: %v\n%s", err, stderr)
	}
	stdout, stderr, err = runInitServerBD(t, repoDir, env, "list", "--json")
	if err != nil {
		t.Fatalf("bd list after adoption failed: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "Adopted workspace issue") {
		t.Fatalf("bd list --json output does not contain the created issue:\n%s", stdout)
	}
}