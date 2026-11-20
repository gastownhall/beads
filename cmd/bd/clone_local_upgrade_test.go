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
	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// cloneLocalUpgradeFixture is a direct-server workspace in the state
// gastownhall/beads#7038 reports after the gate's "pull on the old binary,
// then upgrade" recipe: the server database is at the latest main schema, but
// its clone-local series stopped at 13, before ignored/0016 added
// leases.granted_node, which every issue read selects. The workspace has no
// .beads/dolt directory: the database lives on the server.
type cloneLocalUpgradeFixture struct {
	repo, beadsDir, database string
	admin                    *sql.DB
	bd                       string
}

func newCloneLocalUpgradeFixture(t *testing.T) *cloneLocalUpgradeFixture {
	t.Helper()
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}
	f := &cloneLocalUpgradeFixture{repo: t.TempDir(), database: uniqueTestDBName(t)}
	f.beadsDir = filepath.Join(f.repo, ".beads")
	if err := os.MkdirAll(f.beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   f.database,
	}).Save(f.beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	ctx := context.Background()
	// Path points at a .beads/dolt that is never created: the store lives on
	// the server, and the workspace keeps no local copy of it.
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(f.beadsDir, "dolt"),
		BeadsDir:        f.beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        f.database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(f.database, testDoltServerPort) })
	if err := store.SetConfig(ctx, "issue_prefix", "clu"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	now := time.Now()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: "clu-1", Title: "Readable after the upgrade", Status: types.StatusOpen,
		Priority: 1, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now,
	}, "test-user"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.beadsDir, "dolt")); !os.IsNotExist(err) {
		t.Fatalf("precondition: .beads/dolt must not exist (stat err %v)", err)
	}

	f.admin, err = sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: f.database,
	}.String())
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { f.admin.Close() })
	for _, stmt := range []string{
		"ALTER TABLE leases DROP COLUMN granted_node",
		"DELETE FROM ignored_schema_migrations WHERE version > 13",
	} {
		if _, err := f.admin.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	f.bd = buildBDForInitTests(t)
	return f
}

// stampUpgrade writes the one-shot signal trackBdVersion reads: a stale
// .local_version makes this run an upgrade, arming autoMigrateOnVersionBump.
func (f *cloneLocalUpgradeFixture) stampUpgrade(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.beadsDir, localVersionFile), []byte("1.0.0\n"), 0o600); err != nil {
		t.Fatalf("write stale %s: %v", localVersionFile, err)
	}
}

func (f *cloneLocalUpgradeFixture) run(t *testing.T, extraEnv []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(f.bd, args...)
	cmd.Dir = f.repo
	cmd.Env = append(filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE", schema.AllowRemoteMigrateEnv,
		schema.AllowMigrateEnv, "BD_IGNORE_SCHEMA_SKEW", migration.EnvFreezeFile),
		append([]string{
			"HOME=" + t.TempDir(),
			"XDG_CONFIG_HOME=" + t.TempDir(),
			"BEADS_TEST_MODE=1",
			"BEADS_TEST_SERVER=1",
			"BEADS_DIR=" + f.beadsDir,
			schema.AllowRemoteMigrateEnv + "=0",
		}, extraEnv...)...)
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

// state reads what an upgrade may or may not have changed: the raw cursors
// and whether leases.granted_node exists.
func (f *cloneLocalUpgradeFixture) state(t *testing.T) (mainCursor, ignoredCursor int, grantedNode bool) {
	t.Helper()
	ctx := context.Background()
	var columns int
	for _, q := range []struct {
		query string
		dest  *int
	}{
		{"SELECT COALESCE(MAX(version), 0) FROM schema_migrations", &mainCursor},
		{"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", &ignoredCursor},
		{"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'leases' AND column_name = 'granted_node'", &columns},
	} {
		if err := f.admin.QueryRowContext(ctx, q.query).Scan(q.dest); err != nil {
			t.Fatalf("%s: %v", q.query, err)
		}
	}
	return mainCursor, ignoredCursor, columns == 1
}

// TestServerCloneLocalBehindReadRefusesUntilMigrated covers the reads the
// upgrade hook does not reach: a version signal already consumed or never
// written, strict --readonly and a migration freeze, the last two even with
// the signal present. Each refuses at open with the clone-local refusal and
// changes nothing; the remedy it names, `bd migrate schema`, then converges.
func TestServerCloneLocalBehindReadRefusesUntilMigrated(t *testing.T) {
	f := newCloneLocalUpgradeFixture(t)
	unchanged := func(t *testing.T, what string) {
		t.Helper()
		if mainCursor, ignoredCursor, grantedNode := f.state(t); mainCursor != schema.LatestVersion() || ignoredCursor != 13 || grantedNode {
			t.Errorf("%s changed the database: main v%d, clone-local v%d, granted_node %v", what, mainCursor, ignoredCursor, grantedNode)
		}
	}
	refused := func(t *testing.T, what, stdout, stderr string, code int) {
		t.Helper()
		if code == 0 {
			t.Fatalf("%s succeeded on a database its reads cannot query\nstdout:\n%s\nstderr:\n%s", what, stdout, stderr)
		}
		if !strings.Contains(stderr, "granted_node") || !strings.Contains(stderr, "bd migrate schema") || strings.Contains(stderr, "does not have column") {
			t.Errorf("%s did not refuse at open with the clone-local remedy:\n%s", what, stderr)
		}
	}

	stdout, stderr, code := f.run(t, nil, "list")
	refused(t, "bd list (no version signal)", stdout, stderr, code)
	unchanged(t, "bd list (no version signal)")

	stdout, stderr, code = f.run(t, nil, "list", "--json")
	if code == 0 {
		t.Fatalf("bd list --json succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if out := stdout + stderr; !strings.Contains(out, `"clone_local_schema"`) || !strings.Contains(out, "bd migrate schema") {
		t.Errorf("bd list --json refusal lacks the clone_local_schema detail:\n%s%s", stdout, stderr)
	}

	f.stampUpgrade(t)
	stdout, stderr, code = f.run(t, nil, "--readonly", "list")
	refused(t, "bd --readonly list (upgrade signal present)", stdout, stderr, code)
	unchanged(t, "bd --readonly list")

	freeze := filepath.Join(t.TempDir(), migration.FileName)
	if err := os.WriteFile(freeze, []byte("test freeze\n"), 0o600); err != nil {
		t.Fatalf("write freeze marker: %v", err)
	}
	f.stampUpgrade(t)
	stdout, stderr, code = f.run(t, []string{migration.EnvFreezeFile + "=" + freeze}, "list")
	refused(t, "bd list under a migration freeze (upgrade signal present)", stdout, stderr, code)
	unchanged(t, "bd list under a migration freeze")

	// Back to no signal, so the remedy itself does the work.
	if err := os.Remove(filepath.Join(f.beadsDir, localVersionFile)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", localVersionFile, err)
	}
	stdout, stderr, code = f.run(t, nil, "migrate", "schema")
	if code != 0 {
		t.Fatalf("bd migrate schema exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	stdout, stderr, code = f.run(t, nil, "list")
	if code != 0 || !strings.Contains(stdout, "clu-1") {
		t.Fatalf("bd list after bd migrate schema: exit %d, want the issue listed\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}
