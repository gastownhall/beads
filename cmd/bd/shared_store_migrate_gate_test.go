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

// behindSharedServerWorkspace is a server-mode workspace whose database is one
// migration behind this binary, the upgrade window #5920's refusal exists for.
type behindSharedServerWorkspace struct {
	admin *sql.DB
	run   func(args ...string) (stdout, stderr string, code int)
}

// newBehindSharedServerWorkspace seeds issues into a fresh server-mode
// database and then regresses only the latest schema cursor, as
// TestSharedServerVersionBumpDoesNotPromoteCursor does: the schema itself
// stays applied, so reads keep working and only admission is under test.
// Unlike that test it writes no stale .local_version, so no version-bump
// reconciliation runs and the refusal can only come from the command's own
// open. Migration consent is stripped from the environment and pinned off.
func newBehindSharedServerWorkspace(t *testing.T, prefix string, issues []*types.Issue) behindSharedServerWorkspace {
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
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("create issue %s: %v", issue.ID, err)
		}
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
			"BEADS_ACTOR=tester",
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

	w := behindSharedServerWorkspace{admin: admin, run: run}
	if got := w.cursor(t); got != schema.LatestVersion()-1 {
		t.Fatalf("precondition failed: cursor = %d, want %d", got, schema.LatestVersion()-1)
	}
	return w
}

func (w behindSharedServerWorkspace) cursor(t *testing.T) int {
	t.Helper()
	var v int
	if err := w.admin.QueryRowContext(context.Background(),
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v); err != nil {
		t.Fatalf("read schema cursor: %v", err)
	}
	return v
}

// issueState is an issue's status and assignee, read straight from the
// database so no bd open can migrate anything on the way.
func (w behindSharedServerWorkspace) issueState(t *testing.T, id string) string {
	t.Helper()
	var status string
	var assignee sql.NullString
	if err := w.admin.QueryRowContext(context.Background(),
		"SELECT status, assignee FROM issues WHERE id = ?", id).Scan(&status, &assignee); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return status + "/" + assignee.String
}

// requireSharedRefusal asserts bd args was refused by the shared-store
// migration gate and moved neither the cursor nor the named issues.
func (w behindSharedServerWorkspace) requireSharedRefusal(t *testing.T, args []string, ids ...string) {
	t.Helper()
	before := make(map[string]string, len(ids))
	for _, id := range ids {
		before[id] = w.issueState(t, id)
	}
	cursor := w.cursor(t)
	stdout, stderr, code := w.run(args...)
	if code == 0 {
		t.Errorf("bd %v succeeded on a database one migration behind; writes must stay refused until bd migrate schema\nstdout:\n%s\nstderr:\n%s", args, stdout, stderr)
	} else if !strings.Contains(stderr, "co-resident") || !strings.Contains(stderr, "bd migrate schema") {
		t.Errorf("bd %v refusal is not the shared-store migration refusal:\n%s", args, stderr)
	}
	for _, id := range ids {
		if got := w.issueState(t, id); got != before[id] {
			t.Errorf("bd %v changed %s from %s to %s", args, id, before[id], got)
		}
	}
	if got := w.cursor(t); got != cursor {
		t.Errorf("bd %v moved the schema cursor from %d to %d", args, cursor, got)
	}
}

func behindFixtureIssue(id, title string, priority int) *types.Issue {
	now := time.Now()
	return &types.Issue{
		ID:        id,
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: types.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// TestSharedServerReadyClaimIsRefusedWhileMigrationPending: `bd ready --claim`
// writes, so with a migration pending on a shared server database it gets the
// same refusal `bd update --claim` does, while plain `bd ready` keeps reading
// (bd-fwx149, gastownhall/beads#8264).
func TestSharedServerReadyClaimIsRefusedWhileMigrationPending(t *testing.T) {
	w := newBehindSharedServerWorkspace(t, "rcl", []*types.Issue{
		behindFixtureIssue("rcl-1", "First ready issue", 0),
		behindFixtureIssue("rcl-2", "Second ready issue", 1),
	})

	stdout, stderr, code := w.run("ready")
	if code != 0 || !strings.Contains(stdout, "rcl-1") {
		t.Fatalf("plain bd ready must keep reading on the behind schema: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// The control: the refusal the other two forms must match.
	w.requireSharedRefusal(t, []string{"update", "rcl-2", "--claim"}, "rcl-1", "rcl-2")
	w.requireSharedRefusal(t, []string{"ready", "--claim"}, "rcl-1", "rcl-2")
	w.requireSharedRefusal(t, []string{"ready", "--claim", "--json"}, "rcl-1", "rcl-2")

	// Consent completes the upgrade, and the claim then goes through.
	if stdout, stderr, code := w.run("migrate", "schema"); code != 0 {
		t.Fatalf("bd migrate schema exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout, stderr, code := w.run("ready", "--claim"); code != 0 {
		t.Fatalf("bd ready --claim on the current schema exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := w.issueState(t, "rcl-1"); got != "in_progress/tester" {
		t.Fatalf("after migrating, bd ready --claim left rcl-1 at %s, want in_progress/tester", got)
	}
}

// TestSharedServerDuplicatesAutoMergeIsRefusedWhileMigrationPending is the
// same window for `bd duplicates --auto-merge`: it closes the duplicates, so it
// is refused, while plain `bd duplicates` and the merge's --dry-run preview
// keep reporting the group (bd-fwx149, gastownhall/beads#8264).
func TestSharedServerDuplicatesAutoMergeIsRefusedWhileMigrationPending(t *testing.T) {
	w := newBehindSharedServerWorkspace(t, "dup", []*types.Issue{
		behindFixtureIssue("dup-1", "Same title", 2),
		behindFixtureIssue("dup-2", "Same title", 2),
	})

	for _, args := range [][]string{{"duplicates"}, {"duplicates", "--auto-merge", "--dry-run"}} {
		stdout, stderr, code := w.run(args...)
		if code != 0 || !strings.Contains(stdout, "dup-1") || !strings.Contains(stdout, "dup-2") {
			t.Fatalf("bd %v must keep reporting the group on the behind schema: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
		}
	}

	w.requireSharedRefusal(t, []string{"update", "dup-1", "--claim"}, "dup-1", "dup-2")
	w.requireSharedRefusal(t, []string{"duplicates", "--auto-merge"}, "dup-1", "dup-2")

	if stdout, stderr, code := w.run("migrate", "schema"); code != 0 {
		t.Fatalf("bd migrate schema exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stdout, stderr, code := w.run("duplicates", "--auto-merge"); code != 0 {
		t.Fatalf("bd duplicates --auto-merge on the current schema exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	closed := 0
	for _, id := range []string{"dup-1", "dup-2"} {
		if strings.HasPrefix(w.issueState(t, id), "closed/") {
			closed++
		}
	}
	if closed != 1 {
		t.Fatalf("after migrating, bd duplicates --auto-merge closed %d of the pair, want 1", closed)
	}
}
