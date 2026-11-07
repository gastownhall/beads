//go:build cgo && integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// legacyCursorDDL is the main cursor table as MigrateUp bootstraps it, used to
// stage the empty-cursor shape of an unversioned database.
const legacyCursorDDL = `CREATE TABLE schema_migrations (
	version INT PRIMARY KEY,
	applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	content_hash CHAR(64)
)`

// legacyRoutesDDL and legacyInteractionsDDL are the beads v1.0.0 definitions
// (internal/storage/dolt/schema.go at tag v1.0.0): the tables that SDK's
// migration commit never staged, and that a v1.0.0 seeder such as Gas Town's
// EnsureRigIssuePrefix therefore leaves uncommitted.
const (
	legacyConfigDDL = "CREATE TABLE config (`key` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)"
	legacyRoutesDDL = `CREATE TABLE routes (
	prefix VARCHAR(32) PRIMARY KEY,
	path VARCHAR(512) NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
)`
	legacyInteractionsDDL = `CREATE TABLE interactions (
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
)

// initRecoveryNotePrefix opens the note bd init prints after adopting an
// unversioned dirty database.
const initRecoveryNotePrefix = "Note: database "

// lenientRefusalWarning opens the store-layer warning a lenient open prints
// when it continues past a dirty-table refusal.
const lenientRefusalWarning = "Warning: pending schema migrations alter pre-existing dirty tables"

// halfIdentifiedWarning is the identity warning bd init prints for a database
// with an issue prefix but no project identity, which is how a beads v1.0.0
// seeder leaves it: that release wrote _project_id only from cmd/bd.
const halfIdentifiedWarning = "the database has an issue prefix (hq) but no project identity"

func openUnversionedTestDB(t *testing.T, ctx context.Context, database string) *sql.DB {
	t.Helper()
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", database, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping %s: %v", database, err)
	}
	return db
}

func createUnversionedTestDatabase(t *testing.T, ctx context.Context) string {
	t.Helper()
	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	adminDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+database+"`"); err != nil {
		t.Fatalf("create database %s: %v", database, err)
	}
	return database
}

func execUnversionedFixture(t *testing.T, ctx context.Context, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("fixture: %s: %v", stmt, err)
		}
	}
}

// prepareLegacySeederResidue stages what beads v1.0.0 leaves behind when an
// SDK caller seeds a fresh database: a committed config baseline at its
// schema_version 11, then issue_prefix written to config and the routes and
// interactions tables created (with a row each) and never Dolt-committed.
// There is no migration cursor unless withEmptyCursor commits an empty one.
func prepareLegacySeederResidue(t *testing.T, ctx context.Context, db *sql.DB, withEmptyCursor bool) {
	t.Helper()
	baseline := []string{
		legacyConfigDDL,
		"INSERT INTO config (`key`, value) VALUES ('schema_version', '11')",
	}
	if withEmptyCursor {
		baseline = append(baseline, legacyCursorDDL)
	}
	baseline = append(baseline,
		"CALL DOLT_COMMIT('-Am', 'test: beads v1.0.0 baseline', '--author', 'Beads Test <beads@example.com>')")
	execUnversionedFixture(t, ctx, db, baseline...)
	execUnversionedFixture(t, ctx, db,
		"INSERT INTO config (`key`, value) VALUES ('issue_prefix', 'hq')",
		legacyRoutesDDL,
		"INSERT INTO routes (prefix, path) VALUES ('gt-', 'gastown')",
		legacyInteractionsDDL,
		"INSERT INTO interactions (id, kind, created_at, actor) VALUES ('int-legacy', 'llm_call', NOW(), 'mayor')",
	)

	got := unversionedQueryStrings(t, ctx, db,
		"SELECT CONCAT(table_name, ':', status) FROM dolt_status ORDER BY table_name")
	want := []string{"config:modified", "interactions:new table", "routes:new table"}
	if !slices.Equal(got, want) {
		t.Fatalf("fixture dolt_status = %v, want %v", got, want)
	}
	cursorTables := unversionedQueryInt(t, ctx, db,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'schema_migrations'")
	if wantCursor := map[bool]int{false: 0, true: 1}[withEmptyCursor]; cursorTables != wantCursor {
		t.Fatalf("fixture schema_migrations tables = %d, want %d", cursorTables, wantCursor)
	}
}

func unversionedQueryStrings(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan %q: %v", query, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows %q: %v", query, err)
	}
	return out
}

func unversionedQueryInt(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func unversionedInitArgs(database string, extra ...string) []string {
	return append([]string{"init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "hq"}, extra...)
}

// recoveryCommits returns the hashes of the commits bd init made to adopt the
// working set, newest first.
func recoveryCommits(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	return unversionedQueryStrings(t, ctx, db,
		"SELECT commit_hash FROM dolt_log WHERE message = ?", initReconcileCommitMessage)
}

// TestCLI_InitServerVersionedDirtyWorkingSetStillRefuses_E2E keeps the
// gastownhall/beads#6089 adoption off databases a binary already migrated:
// init against a committed v51 database with an uncommitted issues row, given
// migration consent so the refusal is the #4566 dirty guard and not a consent gate,
// must refuse, make no recovery commit, and leave the row uncommitted at v51.
func TestCLI_InitServerVersionedDirtyWorkingSetStillRefuses_E2E(t *testing.T) {
	skipIfNoDolt(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	database := createUnversionedTestDatabase(t, ctx)
	db := openUnversionedTestDB(t, ctx, database)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin %s: %v", database, err)
	}
	if _, err := schema.MigrateUpTo(ctx, conn, 51); err != nil {
		t.Fatalf("migrate %s to v51: %v", database, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("release %s: %v", database, err)
	}
	execUnversionedFixture(t, ctx, db,
		"CALL DOLT_COMMIT('-Am', 'test: v51 baseline', '--author', 'Beads Test <beads@example.com>')",
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES ('hq-dirty', 'uncommitted issue', '', '', '', '')",
	)

	// The shared-server remote-migrate gate (#5920) also precedes the dirty
	// guard on a versioned database, so consent covers it too.
	env := append(os.Environ(), schema.AllowMigrateEnv+"=1", schema.AllowRemoteMigrateEnv+"=1")
	stdout, stderr, err := runBDExecSeparated(t, testBD, t.TempDir(), env, unversionedInitArgs(database)...)
	if err == nil {
		t.Fatalf("bd init on a versioned dirty database succeeded, want the dirty-table refusal\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "pending schema migrations alter pre-existing dirty tables") || !strings.Contains(stderr, "issues") {
		t.Fatalf("bd init stderr does not carry the dirty-table refusal naming issues:\n%s", stderr)
	}
	if strings.Contains(stderr, initRecoveryNotePrefix) {
		t.Fatalf("bd init printed a recovery note for a refused versioned database:\n%s", stderr)
	}
	if got := recoveryCommits(t, ctx, db); len(got) != 0 {
		t.Fatalf("recovery commits on a versioned database = %v, want none", got)
	}
	if got := unversionedQueryInt(t, ctx, db, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 51 {
		t.Fatalf("main schema version = %d, want 51 (refused, unmigrated)", got)
	}
	if got := unversionedQueryInt(t, ctx, db, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'"); got != 1 {
		t.Fatalf("issues dirty rows in dolt_status = %d, want the pending row still uncommitted", got)
	}
	if got := unversionedQueryInt(t, ctx, db, "SELECT COUNT(*) FROM issues WHERE id = 'hq-dirty'"); got != 1 {
		t.Fatalf("pending issues row count = %d, want it preserved", got)
	}
}

// TestCLI_InitServerUnversionedDirtyWorkingSet_E2E is the user-level pin for
// gastownhall/beads#6089: bd init against a server database a beads v1.0.0
// seeder left with no migration cursor and uncommitted config, routes and
// interactions. Before the fix the strict open's #4566 guard refused it and
// init wrote no workspace configuration, so gt install could not finish
// without a manual `bd dolt commit`. Init now adopts the working set and
// migrates. The seeder wrote an issue prefix but no project identity, and
// init does not complete a half-identified database: it adopts it as found
// and prints its identity warning.
func TestCLI_InitServerUnversionedDirtyWorkingSet_E2E(t *testing.T) {
	skipIfNoDolt(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	database := createUnversionedTestDatabase(t, ctx)
	db := openUnversionedTestDB(t, ctx, database)
	prepareLegacySeederResidue(t, ctx, db, false)

	tmpDir := t.TempDir()
	env := os.Environ()
	stdout, stderr, err := runBDExecSeparated(t, testBD, tmpDir, env, unversionedInitArgs(database)...)
	if err != nil {
		t.Fatalf("bd init on an unversioned dirty database failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if n := strings.Count(stderr, initRecoveryNotePrefix); n != 1 {
		t.Fatalf("stderr carries %d recovery notes, want 1:\n%s", n, stderr)
	}
	if !strings.Contains(stderr, lenientRefusalWarning) {
		t.Fatalf("stderr lacks the lenient-open warning:\n%s", stderr)
	}

	if got := unversionedQueryInt(t, ctx, db, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != schema.LatestVersion() {
		t.Fatalf("main schema version after init = %d, want %d", got, schema.LatestVersion())
	}
	if dirty := unversionedQueryStrings(t, ctx, db, "SELECT table_name FROM dolt_status"); len(dirty) != 0 {
		t.Fatalf("working set after init is dirty: %v", dirty)
	}
	if !strings.Contains(stderr, halfIdentifiedWarning) {
		t.Fatalf("stderr lacks the identity warning %q:\n%s", halfIdentifiedWarning, stderr)
	}
	if n := unversionedQueryInt(t, ctx, db, "SELECT COUNT(*) FROM metadata WHERE `key` = '_project_id'"); n != 0 {
		t.Fatalf("database _project_id rows after init = %d, want 0 (init adopts a half-identified database as found)", n)
	}

	bd := func(args ...string) string {
		t.Helper()
		out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, args...)
		if err != nil {
			t.Fatalf("bd %v failed after init: %v\n%s", args, err, out)
		}
		return out
	}
	created := bd("create", "adopted database works", "--json")
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created), &issue); err != nil || !strings.HasPrefix(issue.ID, "hq-") {
		t.Fatalf("bd create --json = %q (%v), want an hq- issue", created, err)
	}
	if listed := bd("list"); !strings.Contains(listed, issue.ID) {
		t.Fatalf("bd list does not show %s:\n%s", issue.ID, listed)
	}
}

// migrationCommitsAfter checks HEAD's history (dolt_log, newest first) around
// the recovery commit: it returns how many numbered migration commits are
// newer than it, and fails if any is older.
func migrationCommitsAfter(t *testing.T, ctx context.Context, db *sql.DB, recovery string) int {
	t.Helper()
	newer, seen := 0, false
	for _, row := range unversionedQueryStrings(t, ctx, db, "SELECT CONCAT(commit_hash, ' ', message) FROM dolt_log") {
		hash, message, _ := strings.Cut(row, " ")
		switch {
		case hash == recovery:
			seen = true
		case !strings.HasPrefix(message, "schema: apply migration"):
		case seen:
			t.Fatalf("migration commit %q precedes the recovery commit %s", message, recovery)
		default:
			newer++
		}
	}
	if !seen {
		t.Fatalf("recovery commit %s is not in HEAD's history", recovery)
	}
	return newer
}

// TestCLI_InitServerUnversionedDirtyRecoveryCommit_E2E pins the history the
// #6089 adoption leaves: exactly one commit with the fixed recovery message,
// holding the pre-existing changes at the pre-migration schema, followed by
// the numbered migrations and still an ancestor of the final HEAD. A second
// init adopts nothing more.
func TestCLI_InitServerUnversionedDirtyRecoveryCommit_E2E(t *testing.T) {
	skipIfNoDolt(t)
	for _, tc := range []struct {
		name       string
		emptyTable bool
	}{
		{name: "absent cursor table"},
		{name: "empty cursor table", emptyTable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			database := createUnversionedTestDatabase(t, ctx)
			db := openUnversionedTestDB(t, ctx, database)
			prepareLegacySeederResidue(t, ctx, db, tc.emptyTable)

			workDir := t.TempDir()
			stdout, stderr, err := runBDExecSeparated(t, testBD, workDir, os.Environ(), unversionedInitArgs(database)...)
			if err != nil {
				t.Fatalf("bd init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}

			commits := recoveryCommits(t, ctx, db)
			if len(commits) != 1 {
				t.Fatalf("recovery commits = %v, want exactly one", commits)
			}
			hash := commits[0]
			asOf := func(query string) []string {
				return unversionedQueryStrings(t, ctx, db, fmt.Sprintf(query, hash))
			}
			if got := asOf("SELECT value FROM config AS OF '%s' WHERE `key` = 'issue_prefix'"); !slices.Equal(got, []string{"hq"}) {
				t.Fatalf("issue_prefix at the recovery commit = %v, want [hq]", got)
			}
			if got := asOf("SELECT value FROM config AS OF '%s' WHERE `key` = 'schema_version'"); !slices.Equal(got, []string{"11"}) {
				t.Fatalf("schema_version at the recovery commit = %v, want the v1.0.0 baseline [11]", got)
			}
			if got := asOf("SELECT path FROM routes AS OF '%s' WHERE prefix = 'gt-'"); !slices.Equal(got, []string{"gastown"}) {
				t.Fatalf("routes row at the recovery commit = %v, want [gastown]", got)
			}
			if got := asOf("SELECT id FROM interactions AS OF '%s'"); !slices.Equal(got, []string{"int-legacy"}) {
				t.Fatalf("interactions at the recovery commit = %v, want [int-legacy]", got)
			}
			// Before the numbered migrations: no issues table yet, and the
			// cursor exactly as the fixture left it.
			tables := asOf("SHOW TABLES AS OF '%s'")
			if slices.Contains(tables, "issues") {
				t.Fatalf("recovery commit already holds migrated tables: %v", tables)
			}
			if slices.Contains(tables, "schema_migrations") != tc.emptyTable {
				t.Fatalf("recovery commit tables = %v, want schema_migrations present=%v", tables, tc.emptyTable)
			}
			if tc.emptyTable {
				if n := len(asOf("SELECT version FROM schema_migrations AS OF '%s'")); n != 0 {
					t.Fatalf("cursor at the recovery commit has %d rows, want 0", n)
				}
			}
			if base := asOf("SELECT DOLT_MERGE_BASE('%s', 'HEAD')"); !slices.Equal(base, []string{hash}) {
				t.Fatalf("merge base of recovery commit and HEAD = %v, want %s (an ancestor)", base, hash)
			}
			if n := migrationCommitsAfter(t, ctx, db, hash); n == 0 {
				t.Fatal("no numbered migration commit follows the recovery commit")
			}

			// A second init finds a versioned, clean database: no second
			// recovery commit, no note, and the cursor stays at the latest.
			_, stderr, _ = runBDExecSeparated(t, testBD, workDir, os.Environ(), unversionedInitArgs(database)...)
			if strings.Contains(stderr, initRecoveryNotePrefix) {
				t.Fatalf("second bd init printed a recovery note:\n%s", stderr)
			}
			if got := recoveryCommits(t, ctx, db); len(got) != 1 {
				t.Fatalf("recovery commits after a second init = %v, want only %s", got, hash)
			}
			if got := unversionedQueryInt(t, ctx, db, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != schema.LatestVersion() {
				t.Fatalf("main schema version after a second init = %d, want %d", got, schema.LatestVersion())
			}
		})
	}
}

// TestCLI_InitServerUnversionedDirtyOutput_E2E pins where the
// gastownhall/beads#6089 adoption reports itself: the default run prints one
// full note on stderr, --quiet drops the note but keeps the store's lenient
// warning, and --json keeps the note on stderr while stdout decodes as exactly
// one JSON document.
func TestCLI_InitServerUnversionedDirtyOutput_E2E(t *testing.T) {
	skipIfNoDolt(t)

	for _, tc := range []struct {
		name     string
		extra    []string
		wantNote bool
		wantJSON bool
	}{
		{name: "default", wantNote: true},
		{name: "quiet", extra: []string{"--quiet"}},
		{name: "json", extra: []string{"--json"}, wantNote: true, wantJSON: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			database := createUnversionedTestDatabase(t, ctx)
			db := openUnversionedTestDB(t, ctx, database)
			prepareLegacySeederResidue(t, ctx, db, false)

			stdout, stderr, err := runBDExecSeparated(t, testBD, t.TempDir(), os.Environ(), unversionedInitArgs(database, tc.extra...)...)
			if err != nil {
				t.Fatalf("bd init %v failed: %v\nstdout:\n%s\nstderr:\n%s", tc.extra, err, stdout, stderr)
			}

			note := fmt.Sprintf("Note: database %q had uncommitted changes in tables the schema migrations alter (config, interactions, routes); bd init committed the working set in its own commit, then applied the migrations (gastownhall/beads#6089)\n", database)
			wantNotes := 0
			if tc.wantNote {
				wantNotes = 1
			}
			if n := strings.Count(stderr, initRecoveryNotePrefix); n != wantNotes {
				t.Fatalf("stderr carries %d recovery notes, want %d:\n%s", n, wantNotes, stderr)
			}
			if tc.wantNote && !strings.Contains(stderr, note) {
				t.Fatalf("stderr lacks the full recovery note %q:\n%s", note, stderr)
			}
			if strings.Contains(stdout, initRecoveryNotePrefix) {
				t.Fatalf("stdout carries the recovery note:\n%s", stdout)
			}
			if !strings.Contains(stderr, lenientRefusalWarning) {
				t.Fatalf("stderr lacks the lenient-open warning:\n%s", stderr)
			}
			if len(recoveryCommits(t, ctx, db)) != 1 {
				t.Fatalf("recovery commits = %v, want exactly one", recoveryCommits(t, ctx, db))
			}

			if !tc.wantJSON {
				return
			}
			if strings.TrimSpace(stdout) == "" {
				t.Fatalf("bd init --json printed nothing on stdout; stderr:\n%s", stderr)
			}
			dec := json.NewDecoder(strings.NewReader(stdout))
			var summary map[string]any
			if err := dec.Decode(&summary); err != nil {
				t.Fatalf("bd init --json stdout is not JSON: %v\n%s", err, stdout)
			}
			var extra any
			if err := dec.Decode(&extra); err != io.EOF {
				t.Fatalf("bd init --json stdout holds more than one JSON document (%v):\n%s", err, stdout)
			}
			if summary["initialized"] != true || summary["database"] != database || summary["mode"] != "server" {
				t.Fatalf("bd init --json summary = %v, want initialized server-mode %s", summary, database)
			}
		})
	}
}
