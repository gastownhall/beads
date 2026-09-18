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
