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

// behindSharedServerFixture builds a direct-server workspace whose database is
// one migration behind by cursor alone — the same shape
// TestSharedServerVersionBumpDoesNotPromoteCursor regresses — so every open
// has to refuse to migrate it while reads keep working. Seed issues through
// the returned store while the cursor is still current; the helper closes the
// store and regresses the cursor afterwards.
//
// Deliberately NO stale .local_version is written: arming the version-bump
// reconciliation would mix a second migration-refusal source into a test
// about admission at open, so consent stays the only way out
// (BD_ALLOW_REMOTE_MIGRATE=0 in the run env).
func behindSharedServerFixture(t *testing.T, prefix string, seed func(*dolt.DoltStore)) (run func(args ...string) (string, string, int), cursor func() int, behind int) {
	t.Helper()

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
	// SetForceAllowRemoteMigrate is deliberately NOT set: the workspace is
	// created current, so this open migrates nothing, and every later open is
	// expected to refuse the now-pending migration without consent.
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
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	seed(store)
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })

	// The local dolt data dir the auto-start shape of the incident had; an
	// empty one arms no reconciliation of its own (see the note above).
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("create local dolt data dir: %v", err)
	}

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
	cursor = func() int {
		var v int
		if err := admin.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
			t.Fatalf("read schema cursor: %v", err)
		}
		return v
	}
	behind = cursor()

	run = func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(buildBDForInitTests(t), args...)
		cmd.Dir = repo
		cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
			"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv),
			"HOME="+t.TempDir(),
			"XDG_CONFIG_HOME="+t.TempDir(),
			"BEADS_TEST_MODE=1",
			"BEADS_TEST_SERVER=1",
			"BEADS_ACTOR=tester",
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
	return run, cursor, behind
}

// TestSharedServerWriteFlagsOnReadCommandsAreRefused reproduces
// gastownhall/beads#8264 end to end, through the real CLI. `bd ready --claim`
// and `bd duplicates --auto-merge` write, but their leaf names sit on the
// read-only allowlist, so on a shared server one migration behind they opened
// read-only, skipped the migration gate the other writers are refused by, and
// mutated anyway. After the fix they select the writer admission
// `bd update --claim` already gets: same refusal, same untouched cursor, and
// the plain listings plus the merge preview keep working through the window.
// Consent then completes the upgrade and both forms work again on the
// current schema.
func TestSharedServerWriteFlagsOnReadCommandsAreRefused(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	t.Run("ready_claim", func(t *testing.T) {
		const prefix = "wfr"
		run, cursor, behind := behindSharedServerFixture(t, prefix, func(store *dolt.DoltStore) {
			now := time.Now()
			for _, issue := range []*types.Issue{
				{ID: prefix + "-1", Title: "First claimable through the window", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
				{ID: prefix + "-2", Title: "Second claimable through the window", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
			} {
				if err := store.CreateIssue(context.Background(), issue, "test-user"); err != nil {
					t.Fatalf("seed %s: %v", issue.ID, err)
				}
			}
		})

		// The plain listing keeps reading through the window.
		stdout, stderr, code := run("ready")
		if code != 0 {
			t.Fatalf("bd ready exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "First claimable through the window") {
			t.Errorf("bd ready did not render the seeded issue on the old schema:\n%s", stdout)
		}

		// Control: an unambiguous writer is refused with the shared-store
		// guidance.
		stdout, stderr, code = run("update", prefix+"-2", "--claim")
		if code == 0 {
			t.Fatalf("bd update --claim succeeded on a behind shared database\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "co-resident") {
			t.Errorf("bd update --claim refusal missing the shared-store guidance:\n%s", stderr)
		}

		// The affected write: same refusal, nothing claimed, cursor intact.
		stdout, stderr, code = run("ready", "--claim")
		if code == 0 {
			t.Fatalf("bd ready --claim succeeded on a behind shared database\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "co-resident") {
			t.Errorf("bd ready --claim refusal missing the shared-store guidance:\n%s", stderr)
		}
		if got := cursor(); got != behind {
			t.Fatalf("the refused claim promoted the schema cursor from %d to %d", behind, got)
		}
		for _, id := range []string{prefix + "-1", prefix + "-2"} {
			out, _, claimCode := run("show", id, "--json")
			if claimCode != 0 {
				t.Fatalf("bd show %s exit = %d\n%s", id, claimCode, out)
			}
			if !strings.Contains(out, `"status": "open"`) {
				t.Errorf("%s was mutated by a refused ready --claim:\n%s", id, out)
			}
			if strings.Contains(out, `"assignee": "tester"`) {
				t.Errorf("%s was claimed by a refused ready --claim:\n%s", id, out)
			}
		}

		// Consent completes the upgrade and the claim works again.
		stdout, stderr, code = run("migrate", "schema")
		if code != 0 {
			t.Fatalf("bd migrate schema exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if got := cursor(); got != schema.LatestVersion() {
			t.Fatalf("cursor = %d after bd migrate schema, want %d", got, schema.LatestVersion())
		}
		stdout, stderr, code = run("ready", "--claim")
		if code != 0 {
			t.Fatalf("bd ready --claim on the migrated schema exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Claimed issue") {
			t.Errorf("bd ready --claim on the migrated schema did not claim:\n%s", stdout)
		}
	})

	t.Run("duplicates_auto_merge", func(t *testing.T) {
		const prefix = "wfm"
		run, cursor, behind := behindSharedServerFixture(t, prefix, func(store *dolt.DoltStore) {
			now := time.Now()
			for _, id := range []string{prefix + "-1", prefix + "-2"} {
				if err := store.CreateIssue(context.Background(), &types.Issue{
					ID:        id,
					Title:     "Same title through the window",
					Status:    types.StatusOpen,
					Priority:  1,
					IssueType: types.TypeTask,
					CreatedAt: now,
					UpdatedAt: now,
				}, "test-user"); err != nil {
					t.Fatalf("seed %s: %v", id, err)
				}
			}
		})

		// The plain listing and the merge preview keep reading through the
		// window.
		stdout, stderr, code := run("duplicates")
		if code != 0 {
			t.Fatalf("bd duplicates exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, prefix+"-1") || !strings.Contains(stdout, prefix+"-2") {
			t.Errorf("bd duplicates did not report the group on the old schema:\n%s", stdout)
		}
		stdout, stderr, code = run("duplicates", "--auto-merge", "--dry-run", "--json")
		if code != 0 {
			t.Fatalf("bd duplicates --auto-merge --dry-run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "merge_commands") {
			t.Errorf("the dry-run preview did not report the group:\n%s", stdout)
		}
		if strings.Contains(stdout, "merge_results") {
			t.Errorf("the dry-run preview performed merges:\n%s", stdout)
		}

		// The affected write: same refusal as other writers, nothing merged,
		// cursor intact.
		stdout, stderr, code = run("duplicates", "--auto-merge")
		if code == 0 {
			t.Fatalf("bd duplicates --auto-merge succeeded on a behind shared database\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "co-resident") {
			t.Errorf("bd duplicates --auto-merge refusal missing the shared-store guidance:\n%s", stderr)
		}
		if got := cursor(); got != behind {
			t.Fatalf("the refused merge promoted the schema cursor from %d to %d", behind, got)
		}
		for _, id := range []string{prefix + "-1", prefix + "-2"} {
			out, _, showCode := run("show", id, "--json")
			if showCode != 0 {
				t.Fatalf("bd show %s exit = %d\n%s", id, showCode, out)
			}
			if !strings.Contains(out, `"status": "open"`) {
				t.Errorf("%s was mutated by a refused duplicates --auto-merge:\n%s", id, out)
			}
		}

		// Consent completes the upgrade and the merge works again.
		stdout, stderr, code = run("migrate", "schema")
		if code != 0 {
			t.Fatalf("bd migrate schema exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if got := cursor(); got != schema.LatestVersion() {
			t.Fatalf("cursor = %d after bd migrate schema, want %d", got, schema.LatestVersion())
		}
		stdout, stderr, code = run("duplicates", "--auto-merge")
		if code != 0 {
			t.Fatalf("bd duplicates --auto-merge on the migrated schema exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Merged") {
			t.Errorf("bd duplicates --auto-merge on the migrated schema did not merge:\n%s", stdout)
		}
	})
}
