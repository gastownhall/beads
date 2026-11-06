//go:build cgo

package main

import (
	"context"
	"database/sql"
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

// dirtyUpgradeWorkspace is the server-mode workspace of gastownhall/beads#6929:
// a database whose cursor is one migration-pass behind this binary (0073
// alters `issues`), an uncommitted user edit to `issues` in the working set,
// and a .local_version from an older bd, so the next command's version-bump
// reconciliation runs the pending migrations and the dirty-table guard
// refuses them. The schema change itself stays applied, so reads keep working
// and only the reconciliation is at stake.
type dirtyUpgradeWorkspace struct {
	repo     string
	beadsDir string
	witness  string
	admin    *sql.DB
	bd       string
}

const dirtyUpgradeOldWitness = "1.0.0"

func newDirtyUpgradeWorkspace(t *testing.T) *dirtyUpgradeWorkspace {
	t.Helper()
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
	if err := store.SetConfig(ctx, "issue_prefix", "drt"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	now := time.Now()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        "drt-1",
		Title:     "one",
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
	// autoMigrateOnVersionBump skips a workspace with no local database
	// directory.
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

	// Commit the fixture so the only dirt is the user edit below.
	if _, err := admin.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'fixture')"); err != nil &&
		!strings.Contains(err.Error(), "nothing to commit") {
		t.Fatalf("commit fixture: %v", err)
	}
	if schema.LatestVersion() != 74 {
		t.Fatalf("this fixture regresses the cursor to before 0073 (which alters issues); LatestVersion() = %d, retarget it", schema.LatestVersion())
	}
	if _, err := admin.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version >= 73"); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'regress cursor')"); err != nil {
		t.Fatalf("commit regressed cursor: %v", err)
	}
	// The recorded bd_version must not let the read-only probe report the
	// database as already current and skip the writable attempt under test.
	// local_metadata is dolt-ignored, so this leaves the working set clean.
	if _, err := admin.ExecContext(ctx, "DELETE FROM local_metadata WHERE `key` = 'bd_version'"); err != nil &&
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("clear recorded bd_version: %v", err)
	}
	// The user edit that leaves `issues` dirty in the working set.
	if _, err := admin.ExecContext(ctx, "UPDATE issues SET title = 'edited' WHERE id = 'drt-1'"); err != nil {
		t.Fatalf("dirty issues: %v", err)
	}

	witness := filepath.Join(beadsDir, localVersionFile)
	if err := os.WriteFile(witness, []byte(dirtyUpgradeOldWitness+"\n"), 0o600); err != nil {
		t.Fatalf("write stale %s: %v", localVersionFile, err)
	}

	return &dirtyUpgradeWorkspace{
		repo:     repo,
		beadsDir: beadsDir,
		witness:  witness,
		admin:    admin,
		bd:       buildBDForInitTests(t),
	}
}

// run runs bd from dir (the workspace root when empty) with BEADS_DIR naming
// beadsDir (the workspace's when empty) and migration consent given, as the
// #5920 notice tells scripted use to give it.
func (w *dirtyUpgradeWorkspace) run(t *testing.T, dir, beadsDir string, args ...string) (string, string, int) {
	t.Helper()
	if dir == "" {
		dir = w.repo
	}
	if beadsDir == "" {
		beadsDir = w.beadsDir
	}
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = dir
	cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv,
		schema.AllowMigrateEnv, "BD_IGNORE_SCHEMA_SKEW", "BD_DEBUG"),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_MODE=1",
		"BEADS_TEST_SERVER=1",
		"BEADS_DIR="+beadsDir,
		schema.AllowRemoteMigrateEnv+"=1",
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
	t.Logf("bd %v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout.String(), stderr.String())
	return stdout.String(), stderr.String(), code
}

func (w *dirtyUpgradeWorkspace) cursor(t *testing.T) int {
	t.Helper()
	var cursor int
	if err := w.admin.QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&cursor); err != nil {
		t.Fatalf("read schema cursor: %v", err)
	}
	return cursor
}

// assertRefusedAndRetained checks the state a refused version-bump leaves:
// nothing migrated, the user's uncommitted edit intact, and the old witness
// kept so the next command retries.
func (w *dirtyUpgradeWorkspace) assertRefusedAndRetained(t *testing.T, after string) {
	t.Helper()
	if got := w.cursor(t); got != 72 {
		t.Errorf("after %s: schema cursor = %d, want it unchanged at 72", after, got)
	}
	var title string
	if err := w.admin.QueryRowContext(context.Background(),
		"SELECT title FROM issues WHERE id = 'drt-1'").Scan(&title); err != nil {
		t.Fatalf("read drt-1 title: %v", err)
	}
	if title != "edited" {
		t.Errorf("after %s: the uncommitted user edit is gone (title %q)", after, title)
	}
	if got := readLocalVersion(w.witness); got != dirtyUpgradeOldWitness {
		t.Errorf("after %s: %s = %q, want the previous %q kept so the next command retries the upgrade",
			after, localVersionFile, got, dirtyUpgradeOldWitness)
	}
}

// assertDirtyUpgradeNotice checks the dirty-table failure notice: it names the
// dirty table, gives the commit-then-migrate remedy in that order, and comes
// after the consent's "applying" line, which it corrects.
func assertDirtyUpgradeNotice(t *testing.T, cmd, stderr string) {
	t.Helper()
	notice := strings.Index(stderr, "pending schema migrations were not applied")
	if notice < 0 {
		t.Fatalf("%s: stderr does not report the refused version-bump migration:\n%s", cmd, stderr)
	}
	rest := stderr[notice:]
	if !strings.Contains(rest, "issues") {
		t.Errorf("%s: the notice does not name the dirty table:\n%s", cmd, rest)
	}
	commit, migrate := strings.Index(rest, "'bd dolt commit'"), strings.Index(rest, "'bd migrate schema'")
	if commit < 0 || migrate < 0 || commit > migrate {
		t.Errorf("%s: the notice does not give 'bd dolt commit' then 'bd migrate schema':\n%s", cmd, rest)
	}
	if applying := strings.Index(strings.ToLower(stderr), "applying"); applying < 0 || applying > notice {
		t.Errorf("%s: want the consent's applying line, then the notice correcting it:\n%s", cmd, stderr)
	}
}

// TestServerVersionBumpDirtyTablesRefusalIsReportedAndRetried reproduces
// gastownhall/beads#6929 end to end. The operator has consented to migrate
// (BD_ALLOW_REMOTE_MIGRATE=1), so the version-bump reconciliation's writable
// open runs the pending migrations and is refused with
// schema.DirtyTablesError. That refusal used to reach only debug logging, and
// .local_version was already rewritten, so no later command retried it or
// said why `bd list` later failed.
func TestServerVersionBumpDirtyTablesRefusalIsReportedAndRetried(t *testing.T) {
	w := newDirtyUpgradeWorkspace(t)

	_, stderr, _ := w.run(t, "", "", "count")
	assertDirtyUpgradeNotice(t, "bd count", stderr)
	w.assertRefusedAndRetained(t, "bd count")
}
