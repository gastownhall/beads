//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestSharedServerVersionBumpDoesNotPromoteCursor reproduces
// gastownhall/beads#5920 end to end, through the real CLI.
//
// The incident: a shared dolt sql-server with no remote, a team of co-resident
// bd clients, one of them upgraded. That client's first command was a plain
// `bd list` — a read — but the root pre-run's version-bump reconciliation
// (autoMigrateOnVersionBump) opens its OWN writable store, which migrated the
// database and promoted schema_migrations for everyone. Every co-resident
// client still on the old binary then refused the database until it was
// upgraded too, for ~14 minutes, with nothing in the output saying what had
// happened.
//
// The assertion that matters is MAX(version): the read must succeed and the
// cursor must not move.
func TestSharedServerVersionBumpDoesNotPromoteCursor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	database := uniqueTestDBName(t)
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	ctx := context.Background()
	// SetForceAllowRemoteMigrate is deliberately NOT set: this fixture creates
	// the database from scratch, so the gate sees version 0 and allows it —
	// creating a database is consent for its schema.
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "shr"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	now := time.Now()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        "shr-1",
		Title:     "Readable through the upgrade window",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}, "test-user"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	// The version-bump reconciliation only runs when the workspace has a local
	// Dolt data dir (it stats cfg.DatabasePath before opening anything) — the
	// shape of the auto-started localhost server the incident was reported on.
	// The bytes live in the test container, but the directory is what arms the
	// code path under test.
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("create local dolt data dir: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database,
	}.String())
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { admin.Close() })

	// Put the database one migration behind the candidate binary by regressing
	// only the recorded cursor — the schema change itself stays applied, so
	// reads keep working, exactly as they did throughout the incident.
	if _, err := admin.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	cursor := func() int {
		var v int
		if err := admin.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
			t.Fatalf("read schema cursor: %v", err)
		}
		return v
	}
	behind := cursor()
	if behind != schema.LatestVersion()-1 {
		t.Fatalf("precondition failed: cursor = %d, want %d", behind, schema.LatestVersion()-1)
	}

	// The one-shot upgrade signal: a stale .local_version makes trackBdVersion
	// report a version bump, which is what arms autoMigrateOnVersionBump. The
	// value has to be strictly below the candidate binary's Version and still
	// in the 1.x era — a pre-1.0 witness is refused outright by the cross-era
	// legacy guard long before any of this runs.
	if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("1.0.0\n"), 0o600); err != nil {
		t.Fatalf("write stale %s: %v", localVersionFile, err)
	}

	bd := buildBDForInitTests(t)
	run := func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = repo
		cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
			"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv),
			"HOME="+t.TempDir(),
			"XDG_CONFIG_HOME="+t.TempDir(),
			"BEADS_TEST_MODE=1",
			"BEADS_TEST_SERVER=1",
			"BEADS_DIR="+beadsDir,
			schema.AllowRemoteMigrateEnv+"=0",
		)
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("running bd %v: %v", args, err)
			}
			code = exitErr.ExitCode()
		}
		return stdout.String(), stderr.String(), code
	}

	stdout, stderr, code := run("list")
	if code != 0 {
		t.Fatalf("bd list exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "shr-1") {
		t.Errorf("bd list did not render the issue; reads must keep working on the old schema:\n%s", stdout)
	}
	if got := cursor(); got != behind {
		t.Fatalf("bd list promoted the schema cursor from %d to %d — this IS gastownhall/beads#5920", behind, got)
	}
	// The #5920 UX gap, inverted: nothing advanced, and the one-shot line says so.
	if !strings.Contains(stderr, "not auto-applying") || !strings.Contains(stderr, "bd migrate schema") {
		t.Errorf("expected the one-shot shared-store notice on stderr, got:\n%s", stderr)
	}

	// A write is refused outright, with the full guidance block rather than a
	// bare error.
	stdout, stderr, code = run("create", "Should be refused", "-p", "2")
	if code == 0 {
		t.Fatalf("bd create succeeded on a behind shared database\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "co-resident") {
		t.Errorf("bd create refusal missing the shared-store guidance:\n%s", stderr)
	}
	if got := cursor(); got != behind {
		t.Fatalf("the refused write promoted the schema cursor from %d to %d", behind, got)
	}

	// Consent completes the upgrade.
	stdout, stderr, code = run("migrate", "schema")
	if code != 0 {
		t.Fatalf("bd migrate schema exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := cursor(); got != schema.LatestVersion() {
		t.Fatalf("cursor = %d after bd migrate schema, want %d", got, schema.LatestVersion())
	}
	// #7283: the run above migrated during the open (its own explicit pass
	// counted zero), so it must say so — not the "already at vN" no-op line
	// that used to hide the work on the very run that did it.
	if !strings.Contains(stdout, "Applied 1 schema migration(s)") {
		t.Errorf("bd migrate schema reported %q, want the Applied/1 line (gastownhall/beads#7283):\n%s", stdout, stdout)
	}
	// The version-bump reconciliation this command stands in for was refused
	// (and its one-shot .local_version signal consumed) by the very gate this
	// command satisfied, so without a stamp bd doctor and the git-hook health
	// check keep reporting a mismatch after the operator did everything the
	// refusal told them to.
	var marker string
	if err := admin.QueryRowContext(ctx,
		"SELECT value FROM local_metadata WHERE `key` = 'bd_version'").Scan(&marker); err != nil {
		t.Fatalf("read bd_version marker after bd migrate schema: %v", err)
	}
	if marker != Version {
		t.Errorf("bd_version marker = %q after bd migrate schema, want %q (gastownhall/beads#7283)", marker, Version)
	}

	// The same repair must not depend on migrations being pending: a stale
	// marker on an already-current database is healed by a no-op run too —
	// the count-based stamp could not (another client migrated while this
	// workspace stood refused, and the upgrade signal is consumed).
	if _, err := admin.ExecContext(ctx,
		"UPDATE local_metadata SET value = '1.0.0' WHERE `key` = 'bd_version'"); err != nil {
		t.Fatalf("regress bd_version marker: %v", err)
	}
	stdout, stderr, code = run("migrate", "schema")
	if code != 0 {
		t.Fatalf("second bd migrate schema exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Schema already at v") {
		t.Errorf("second bd migrate schema reported %q, want the no-op line:\n%s", stdout, stdout)
	}
	if err := admin.QueryRowContext(ctx,
		"SELECT value FROM local_metadata WHERE `key` = 'bd_version'").Scan(&marker); err != nil {
		t.Fatalf("read bd_version marker after the no-op run: %v", err)
	}
	if marker != Version {
		t.Errorf("bd_version marker = %q after the no-op bd migrate schema, want %q (gastownhall/beads#7283)", marker, Version)
	}
}

// TestServerModeReadRefusesASchemaItsReadsCannotQuery is the other half of the
// upgrade window above. There the database was behind by its cursor alone and
// `bd list` had to keep working. Here it is behind for real: the database a
// previous release migrated has no arrival_seq column, which this binary's
// issue reads select. A read-only open never migrates, so without a check at
// open `bd list` failed on the unknown column. It must refuse at open instead,
// naming the remedy, and that remedy must converge.
func TestServerModeReadRefusesASchemaItsReadsCannotQuery(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	database := uniqueTestDBName(t)
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "rdb"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	now := time.Now()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        "rdb-1",
		Title:     "Readable once migrated",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}, "test-user"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database,
	}.String())
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { admin.Close() })

	// The v72 shape a previous release leaves: 0073 and 0074 never ran, so
	// neither plane has the column, neither id tombstone table exists and the
	// cursor stops at 72. Committed, as that release's own migration pass
	// committed it.
	for _, stmt := range []string{
		"ALTER TABLE issues DROP COLUMN arrival_seq",
		"ALTER TABLE wisps DROP COLUMN arrival_seq",
		"DROP TABLE id_tombstones",
		"DROP TABLE wisp_id_tombstones",
		"DELETE FROM schema_migrations WHERE version >= 73",
		"CALL DOLT_COMMIT('-Am', 'test: the schema a v72 release leaves')",
	} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if schema.LatestVersion() != 74 {
		t.Fatalf("this fixture rebuilds the v72 shape for migrations 0073 (arrival_seq, which this binary's reads select) and 0074 (the id tombstone tables); LatestVersion() = %d, retarget it", schema.LatestVersion())
	}

	bd := buildBDForInitTests(t)
	run := func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = repo
		cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
			"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv,
			schema.AllowMigrateEnv, "BD_IGNORE_SCHEMA_SKEW"),
			"HOME="+t.TempDir(),
			"XDG_CONFIG_HOME="+t.TempDir(),
			"BEADS_TEST_MODE=1",
			"BEADS_TEST_SERVER=1",
			"BEADS_DIR="+beadsDir,
			schema.AllowRemoteMigrateEnv+"=0",
		)
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("running bd %v: %v", args, err)
			}
			code = exitErr.ExitCode()
		}
		return stdout.String(), stderr.String(), code
	}

	for _, args := range [][]string{{"list"}, {"show", "rdb-1"}, {"ready"}} {
		stdout, stderr, code := run(args...)
		if code == 0 {
			t.Fatalf("bd %v succeeded on a schema its reads cannot query\nstdout:\n%s\nstderr:\n%s", args, stdout, stderr)
		}
		if !strings.Contains(stderr, "schema version mismatch: database is at v72, binary expects v74") ||
			!strings.Contains(stderr, "bd migrate schema") {
			t.Errorf("bd %v refusal does not name the behind schema and its remedy:\n%s", args, stderr)
		}
		if strings.Contains(stderr, "column") {
			t.Errorf("bd %v reached a query and failed on the missing column instead of refusing at open:\n%s", args, stderr)
		}
	}

	stdout, stderr, code := run("migrate", "schema")
	if code != 0 {
		t.Fatalf("bd migrate schema exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	stdout, stderr, code = run("list")
	if code != 0 || !strings.Contains(stdout, "rdb-1") {
		t.Fatalf("bd list after bd migrate schema: exit %d, want the issue listed\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// TestSharedServerMigrateSchemaReportsTheMigrationsItApplied reproduces
// gastownhall/beads#7283: on a shared server `bd migrate schema` applies the
// pending migrations during the store open (under the verb's consent), so
// runMigrateSchema's own ApplySchemaMigrations counts 0, reports "current",
// and skips the applied branch that stamps the workspace version.
func TestSharedServerMigrateSchemaReportsTheMigrationsItApplied(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	database := uniqueTestDBName(t)
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "shr"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database,
	}.String())
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}

	bd := buildBDForInitTests(t)
	cmd := exec.Command(bd, "migrate", "schema", "--json")
	cmd.Dir = repo
	cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_MODE=1",
		"BEADS_TEST_SERVER=1",
		"BEADS_DIR="+beadsDir,
		schema.AllowRemoteMigrateEnv+"=0",
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd migrate schema --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	var cursor int
	if err := admin.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&cursor); err != nil {
		t.Fatalf("read schema cursor: %v", err)
	}
	if cursor != schema.LatestVersion() {
		t.Fatalf("cursor = %d after bd migrate schema, want %d", cursor, schema.LatestVersion())
	}

	var result struct {
		Status  string `json:"status"`
		Applied int    `json:"applied"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatalf("parse --json output %q: %v", stdout.String(), err)
	}
	if result.Status != "applied" || result.Applied < 1 {
		t.Fatalf("bd migrate schema migrated the database (cursor now v%d) but reported status=%q applied=%d; want status=\"applied\" applied>=1 (gastownhall/beads#7283)\nstdout:\n%s\nstderr:\n%s",
			cursor, result.Status, result.Applied, stdout.String(), stderr.String())
	}
}

// TestSharedServerMigrateSchemaCountsVersionBumpOpen pins the third surface of
// gastownhall/beads#7283: the same shared-server open-time migration, but
// performed by the pre-open autoMigrateOnVersionBump maintenance rather than
// the main open. A fresh install upgrades the binary and — armed by the
// stale .local_version the previous release left — runs `bd migrate schema`
// as its very first command. The maintenance open migrates under the verb's
// consent and closes before the main open, so neither the main open's count
// nor the explicit pass sees it: only the maintenance's return value can
// total it into the report, which used to say "already at vN" on the very run
// that migrated.
func TestSharedServerMigrateSchemaCountsVersionBumpOpen(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	database := uniqueTestDBName(t)
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "vbo"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database,
	}.String())
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { admin.Close() })

	// One migration behind, and a stale workspace marker so the maintenance's
	// read-only probe does not skip the writable open — the marker is also
	// what the migration has to reconcile once it runs.
	if _, err := admin.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := admin.ExecContext(ctx,
		"UPDATE local_metadata SET value = '1.0.0' WHERE `key` = 'bd_version'"); err != nil {
		t.Fatalf("regress bd_version marker: %v", err)
	}

	// The one-shot upgrade signal that arms autoMigrateOnVersionBump for the
	// very command under test. The local dolt data dir above is what makes
	// the maintenance's precondition (DatabasePath exists) hold on a shared
	// server workspace.
	if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("1.0.0\n"), 0o600); err != nil {
		t.Fatalf("write stale %s: %v", localVersionFile, err)
	}

	bd := buildBDForInitTests(t)
	cmd := exec.Command(bd, "migrate", "schema")
	cmd.Dir = repo
	cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_MODE=1",
		"BEADS_TEST_SERVER=1",
		"BEADS_DIR="+beadsDir,
		schema.AllowRemoteMigrateEnv+"=0",
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd migrate schema: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// The maintenance's open is the one that migrated (the main open found
	// the database current), so its shared-store warning is the witness.
	if !strings.Contains(stderr.String(), "applying 1 pending schema migration(s) to a shared server database") {
		t.Errorf("the maintenance open did not migrate under the verb's consent, or its warning is missing:\nstderr:\n%s", stderr.String())
	}
	// And the command that caused it must say so, not "already at vN".
	if !strings.Contains(stdout.String(), "Applied 1 schema migration(s)") {
		t.Errorf("bd migrate schema reported %q, want the Applied/1 line (gastownhall/beads#7283):\n%s", stdout.String(), stdout.String())
	}

	var cursor int
	if err := admin.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&cursor); err != nil {
		t.Fatalf("read schema cursor: %v", err)
	}
	if cursor != schema.LatestVersion() {
		t.Fatalf("cursor = %d after bd migrate schema, want %d", cursor, schema.LatestVersion())
	}
}
