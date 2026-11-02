//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
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

// initServerTestRecoveryCommitHash returns the single recovery commit's
// hash. dolt_log lists ancestors of the current HEAD, newest first, so a
// hash found here is by definition on HEAD's history.
func initServerTestRecoveryCommitHash(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message = ?", initUnversionedRecoveryCommitMessage)
	if err != nil {
		t.Fatalf("read recovery commit hash: %v", err)
	}
	defer rows.Close()
	var hash string
	for rows.Next() {
		if hash != "" {
			t.Fatal("more than one recovery commit found")
		}
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan recovery commit hash: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read recovery commit hash: %v", err)
	}
	if hash == "" {
		t.Fatal("no recovery commit found")
	}
	return hash
}

// initServerTestConfigValueAt reads one config row as of a commit.
func initServerTestConfigValueAt(t *testing.T, db *sql.DB, hash, key string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var value string
	err := db.QueryRowContext(ctx, "SELECT value FROM config AS OF ? WHERE `key` = ?", hash, key).Scan(&value)
	if err != nil {
		t.Fatalf("read config %q as of %s: %v", key, hash, err)
	}
	return value
}

// initServerTestInteractionsRowExistsAt reports whether the fixture's
// residue row is present in interactions as of a commit.
func initServerTestInteractionsRowExistsAt(t *testing.T, db *sql.DB, hash string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM interactions AS OF ? WHERE id = 'ia-1'", hash).Scan(&count); err != nil {
		t.Fatalf("read interactions as of %s: %v", hash, err)
	}
	return count > 0
}

// initServerTestRecoveryCommitPrecedesMigrations walks dolt_log (ancestors
// of HEAD, newest first) and reports whether the recovery commit appears
// after — i.e. earlier in history than — every "schema: apply migration"
// commit: the adoption commit must precede the migrations it unblocked.
func initServerTestRecoveryCommitPrecedesMigrations(t *testing.T, db *sql.DB, recoveryHash string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	recoveryIdx := -1
	for rows.Next() {
		var hash, message string
		if err := rows.Scan(&hash, &message); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		if hash == recoveryHash {
			recoveryIdx++
			continue
		}
		if recoveryIdx >= 0 && strings.HasPrefix(message, "schema: apply migration") {
			// A migration commit newer than the recovery commit — the
			// recovery commit must precede every one of them.
			t.Fatalf("recovery commit %s does not precede migration commit %q", recoveryHash, message)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	return recoveryIdx >= 0
}

// TestCLI_InitServerUnversionedDirtyWorkingSet_E2E pins the #6089 adoption:
// bd init against the unversioned dirty legacy shape commits the
// pre-existing working set in its own labeled commit, applies the pending
// migrations, and leaves a fully working workspace — without any
// migration-consent or shared-store unlock in the environment, exactly
// like the reporter's `gt install` (a cursor of 0 passes both gates).
// The absent-cursor subtest covers the reporter's shape (no
// schema_migrations table); the empty-cursor subtest covers the same
// classification for a cursor table that exists but records nothing.
func TestCLI_InitServerUnversionedDirtyWorkingSet_E2E(t *testing.T) {
	skipIfNoDolt(t)

	t.Run("absent cursor table", func(t *testing.T) {
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

		// The pre-existing working set landed in exactly one labeled
		// recovery commit, carrying the legacy residue rows, and it
		// precedes the migrations it unblocked (dolt_log lists HEAD's
		// ancestors, so presence here already means it is on HEAD's
		// history).
		if got := initServerTestRecoveryCommitCount(t, db); got != 1 {
			t.Fatalf("recovery commits = %d, want exactly 1", got)
		}
		recoveryHash := initServerTestRecoveryCommitHash(t, db)
		if got := initServerTestConfigValueAt(t, db, recoveryHash, "issue_prefix"); got != "hq" {
			t.Fatalf("issue_prefix in the recovery commit = %q, want the legacy residue %q", got, "hq")
		}
		if got := initServerTestConfigValueAt(t, db, recoveryHash, "schema_version"); got != "11" {
			t.Fatalf("schema_version in the recovery commit = %q, want the legacy baseline %q", got, "11")
		}
		if !initServerTestInteractionsRowExistsAt(t, db, recoveryHash) {
			t.Fatal("the legacy residue interactions row is missing from the recovery commit")
		}
		if !initServerTestRecoveryCommitPrecedesMigrations(t, db, recoveryHash) {
			t.Fatalf("recovery commit %s is not an ancestor of HEAD", recoveryHash)
		}

		// A second init is the benign already-initialized refusal, and it
		// must not adopt again: still exactly one recovery commit, cursor
		// unmoved.
		_, stderr, err = runInitServerBD(t, repoDir, env, initServerTestArgs(database)...)
		if err == nil {
			t.Fatal("second bd init succeeded, want the already-initialized refusal")
		}
		if !strings.Contains(stderr, "already initialized") {
			t.Fatalf("second bd init stderr = %q, want the already-initialized refusal", stderr)
		}
		if got := initServerTestRecoveryCommitCount(t, db); got != 1 {
			t.Fatalf("recovery commits after second init = %d, want 1 (no re-adoption)", got)
		}
		if got, want := initServerTestMainCursor(t, db), schema.LatestVersion(); got != want {
			t.Fatalf("main cursor after second init = %d, want %d", got, want)
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
	})

	t.Run("empty cursor table", func(t *testing.T) {
		env := externalServerTestEnv(t)

		repoDir := t.TempDir()
		initGitRepo(t, repoDir)

		database := uniqueTestDBName(t)
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
		db := createInitServerTestDatabase(t, database)
		initServerTestUnversionedLegacyResidue(t, db)

		// An empty schema_migrations table — the bootstrap shape with
		// nothing recorded — classifies as unversioned exactly like the
		// absent table does.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx,
			"CREATE TABLE IF NOT EXISTS schema_migrations (\n"+
				"    version INT PRIMARY KEY,\n"+
				"    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,\n"+
				"    content_hash CHAR(64)\n"+
				")"); err != nil {
			t.Fatalf("create empty schema_migrations: %v", err)
		}

		// Preconditions: the cursor table exists but records nothing.
		if got := initServerTestMainCursor(t, db); got != 0 {
			t.Fatalf("main cursor = %d, want 0 (empty cursor table)", got)
		}
		var cursorTableExists bool
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) > 0 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'schema_migrations'",
		).Scan(&cursorTableExists); err != nil || !cursorTableExists {
			t.Fatalf("schema_migrations probe = (%v, %v), want the table to exist", cursorTableExists, err)
		}

		stdout, stderr, err := runInitServerBD(t, repoDir, env, initServerTestArgs(database)...)
		if err != nil {
			t.Fatalf("bd init on an empty-cursor dirty database failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if got, want := initServerTestMainCursor(t, db), schema.LatestVersion(); got != want {
			t.Fatalf("main cursor = %d, want %d (the pending migrations must have run)", got, want)
		}
		if got := initServerTestRecoveryCommitCount(t, db); got != 1 {
			t.Fatalf("recovery commits = %d, want exactly 1", got)
		}
	})
}

// initUnversionedAdoptionNoteText is the full stderr note line for the
// fixture database and its dirty tables.
func initUnversionedAdoptionNoteText(database string) string {
	return fmt.Sprintf("Note: database %q had uncommitted changes in tables the schema migrations alter (%s); "+
		"bd init committed the working set in its own commit, then applied the migrations; "+
		"no further action is needed (gastownhall/beads#6089)", database, "config, interactions, routes")
}

// TestCLI_InitServerUnversionedAdoptionNote_E2E pins the #6089 recovery
// note: after a successful adoption bd init explains on stderr what it
// committed, --quiet suppresses the note (and only the note — the lenient
// open's own warning is unchanged behavior, not quiet-gated), and --json
// keeps stdout a single JSON document with the note on stderr.
func TestCLI_InitServerUnversionedAdoptionNote_E2E(t *testing.T) {
	skipIfNoDolt(t)

	// runInitOnUnversionedLegacyResidue builds one fresh #6089 fixture and
	// runs bd init on it with extra, returning stdout and stderr.
	runInitOnUnversionedLegacyResidue := func(t *testing.T, extra ...string) (stdout, stderr, database string) {
		t.Helper()
		env := externalServerTestEnv(t)
		repoDir := t.TempDir()
		initGitRepo(t, repoDir)
		database = uniqueTestDBName(t)
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
		db := createInitServerTestDatabase(t, database)
		initServerTestUnversionedLegacyResidue(t, db)
		stdout, stderr, err := runInitServerBD(t, repoDir, env, initServerTestArgs(database, extra...)...)
		if err != nil {
			t.Fatalf("bd init on an unversioned dirty database failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		return stdout, stderr, database
	}

	t.Run("default prints the note to stderr", func(t *testing.T) {
		stdout, stderr, database := runInitOnUnversionedLegacyResidue(t)
		if note := initUnversionedAdoptionNoteText(database); !strings.Contains(stderr, note) {
			t.Fatalf("bd init stderr = %q, want the adoption note:\n%s", stderr, note)
		}
		if strings.Contains(stdout, "Note: database") {
			t.Fatalf("bd init stdout = %q, want the note on stderr only", stdout)
		}
	})

	t.Run("quiet suppresses the note", func(t *testing.T) {
		_, stderr, _ := runInitOnUnversionedLegacyResidue(t, "--quiet")
		if strings.Contains(stderr, "Note: database") {
			t.Fatalf("bd init --quiet stderr = %q, want the adoption note suppressed", stderr)
		}
	})

	t.Run("json keeps the note off stdout", func(t *testing.T) {
		stdout, stderr, database := runInitOnUnversionedLegacyResidue(t, "--json")
		if note := initUnversionedAdoptionNoteText(database); !strings.Contains(stderr, note) {
			t.Fatalf("bd init --json stderr = %q, want the adoption note on stderr", stderr)
		}
		if strings.Contains(stdout, "Note: database") {
			t.Fatalf("bd init --json stdout = %q, want the note on stderr only", stdout)
		}
		if !strings.Contains(stdout, "bd initialized successfully") {
			t.Fatalf("bd init --json stdout = %q, want the normal success summary", stdout)
		}
	})
}
