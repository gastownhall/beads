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

// staleVersionWitness writes the one-shot .local_version that makes the next
// bd command run its post-upgrade reconciliation: a recorded version below
// this binary's arms versionUpgradeDetected for exactly that one command
// (trackBdVersion rewrites the file as it fires).
func staleVersionWitness(t *testing.T, beadsDir, previous string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte(previous+"\n"), 0o600); err != nil {
		t.Fatalf("write stale %s: %v", localVersionFile, err)
	}
}

// TestServerVersionBumpWithoutLocalDoltDir is gastownhall/beads#7038's AC-2
// end to end. A server-mode workspace keeps its database on the dolt
// sql-server and routinely has NO local Dolt data directory — that is the
// shared-server consumer shape the issue is about. The version-bump
// reconciliation used to treat the absent directory as "no database" and
// return before ever opening the server's database, so after the documented
// pull-then-upgrade recipe the clone-local (dolt-ignored) schema series never
// arrived and every read-only command failed on its missing columns. The
// local-directory prerequisite must stay for genuinely local backends, but a
// server-mode workspace's database is the server's, and only the factory open
// (CreateIfMissing stays false) may say it is missing.
//
// Every phase below deliberately creates NO local dolt data dir.
//
// The pre-v0.56 recovery edge of this change — a server-mode workspace must
// never run the .dolt-deleting recovery against its local path — cannot be
// reached through the CLI: its witness is by definition pre-1.0, and the
// cross-era legacy guard refuses such a workspace at admission. It is pinned
// in-process by TestAutoMigrateOnVersionBump_ServerModeSkipsPreV56Recovery.
func TestServerVersionBumpWithoutLocalDoltDir(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	bd := buildBDForInitTests(t)
	// newServerWorkspace builds the #7038 workspace: a server-mode config, a
	// database on the test server with one readable issue, and a run helper
	// whose environment strips every BEADS_/BD_ variable that could reroute
	// the CLI away from that config. No local dolt data dir is created.
	newServerWorkspace := func(prefix string) (beadsDir string, admin *sql.DB, run func(args ...string) (string, string, int)) {
		repo := t.TempDir()
		beadsDir = filepath.Join(repo, ".beads")
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
		if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}
		now := time.Now()
		if err := store.CreateIssue(ctx, &types.Issue{
			ID:        prefix + "-1",
			Title:     "Readable once the upgrade completes",
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
		admin, err = sql.Open("mysql", doltutil.ServerDSN{
			Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: database,
		}.String())
		if err != nil {
			t.Fatalf("connect to test dolt server: %v", err)
		}
		t.Cleanup(func() { admin.Close() })
		run = func(args ...string) (string, string, int) {
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
				"BD_DEBUG=1",
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
		return beadsDir, admin, run
	}

	t.Run("repairs a behind clone-local series", func(t *testing.T) {
		beadsDir, admin, run := newServerWorkspace("svb")
		ctx := context.Background()

		// The #7038 database shape: main series current (the pull brought it),
		// clone-local series behind with granted_node — ignored/0016 — missing,
		// exactly what `bd dolt pull` on a pre-0016 binary leaves. Both objects
		// are dolt-ignored, so the working set stays clean.
		for _, stmt := range []string{
			"ALTER TABLE leases DROP COLUMN granted_node",
			"DELETE FROM ignored_schema_migrations WHERE version > 13",
		} {
			if _, err := admin.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		staleVersionWitness(t, beadsDir, "1.0.0")

		stdout, stderr, code := run("list")
		if code != 0 || !strings.Contains(stdout, "svb-1") {
			t.Fatalf("bd list after the upgrade = exit %d, want the reconciliation's authorized maintenance open to repair the clone-local series so the read works\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		var ignored int
		if err := admin.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&ignored); err != nil {
			t.Fatalf("read ignored cursor: %v", err)
		}
		if ignored != schema.LatestIgnoredVersion() {
			t.Errorf("ignored cursor = %d after the reconciliation, want %d: the hook must reach the server's database and apply the clone-local series", ignored, schema.LatestIgnoredVersion())
		}
		var granted int
		if err := admin.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'leases' AND column_name = 'granted_node'").Scan(&granted); err != nil {
			t.Fatalf("probe granted_node: %v", err)
		}
		if granted != 1 {
			t.Errorf("granted_node column count = %d after the reconciliation, want 1", granted)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(err) {
			t.Errorf("the reconciliation created a local dolt data directory at %s; a server-mode workspace keeps no database there", filepath.Join(beadsDir, "dolt"))
		}
	})

	t.Run("still refuses to promote a shared database's main cursor", func(t *testing.T) {
		beadsDir, admin, run := newServerWorkspace("shg")
		ctx := context.Background()

		// The #5920 shape: main series behind by its cursor alone, so reads
		// keep working while the co-resident clients decide who migrates.
		if _, err := admin.ExecContext(ctx,
			"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
			t.Fatalf("regress schema_migrations: %v", err)
		}
		behind := schema.LatestVersion() - 1
		staleVersionWitness(t, beadsDir, "1.0.0")

		stdout, stderr, code := run("list")
		if code != 0 || !strings.Contains(stdout, "shg-1") {
			t.Fatalf("bd list on a cursor-behind shared database = exit %d, want a working read\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "not auto-applying") || !strings.Contains(stderr, "bd migrate schema") {
			t.Errorf("expected the one-shot shared-store refusal notice on stderr — reaching the gate without a local directory must surface it, not silence it:\n%s", stderr)
		}
		var cursor int
		if err := admin.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&cursor); err != nil {
			t.Fatalf("read schema cursor: %v", err)
		}
		if cursor != behind {
			t.Fatalf("bd list promoted the shared schema cursor from %d to %d — this IS gastownhall/beads#5920", behind, cursor)
		}
	})

	t.Run("does not create a missing target database", func(t *testing.T) {
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
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
		staleVersionWitness(t, beadsDir, "1.0.0")

		bd := buildBDForInitTests(t)
		cmd := exec.Command(bd, "list")
		cmd.Dir = repo
		cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
			"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv,
			schema.AllowMigrateEnv, "BD_IGNORE_SCHEMA_SKEW"),
			"HOME="+t.TempDir(),
			"XDG_CONFIG_HOME="+t.TempDir(),
			"BEADS_TEST_MODE=1",
			"BEADS_TEST_SERVER=1",
			"BEADS_DIR="+beadsDir,
			"BD_DEBUG=1",
			schema.AllowRemoteMigrateEnv+"=0",
		)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running bd list: %v\nstderr:\n%s", err, stderr.String())
		}

		ctx := context.Background()
		admin, err := sql.Open("mysql", doltutil.ServerDSN{
			Host: "127.0.0.1", Port: testDoltServerPort, User: "root",
		}.String())
		if err != nil {
			t.Fatalf("connect to test dolt server: %v", err)
		}
		defer func() { admin.Close() }()
		var created int
		if err := admin.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", database).Scan(&created); err != nil {
			t.Fatalf("probe schemata: %v", err)
		}
		if created != 0 {
			t.Errorf("the reconciliation created database %s; the maintenance open must keep CreateIfMissing off", database)
		}
		if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(err) {
			t.Errorf("the reconciliation created a local dolt data directory at %s", filepath.Join(beadsDir, "dolt"))
		}
	})
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
