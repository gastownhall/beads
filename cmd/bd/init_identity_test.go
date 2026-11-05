//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/types"
)

// GH#6172: bd init's guards must reason about the database the workspace is
// configured for, at its configured endpoint, not whatever --database,
// --server-host/--server-port or an exported BEADS_DOLT_SERVER_* names. These
// tests drive the real binary against the package's test Dolt server.

// repointWorkspace is a server-mode workspace initialized against the test
// server, recording database DB.
type repointWorkspace struct {
	repoDir  string
	beadsDir string
	env      []string
	db       string
}

// newRepointWorkspace initializes a server-mode workspace recording a fresh
// database and seeds it with issues issues.
func newRepointWorkspace(t *testing.T, issues int) repointWorkspace {
	t.Helper()
	skipIfNoDolt(t)
	w := repointWorkspace{repoDir: t.TempDir(), env: externalServerTestEnv(t), db: uniqueTestDBName(t)}
	w.beadsDir = filepath.Join(w.repoDir, ".beads")
	initGitRepo(t, w.repoDir)
	t.Cleanup(func() { dropTestDatabase(w.db, testDoltServerPort) })
	if out, err := runExternalServerBD(t, w.repoDir, w.env, externalServerInitArgs(w.db)...); err != nil {
		t.Fatalf("bd init of %s failed: %v\n%s", w.db, err, out)
	}
	for i := 0; i < issues; i++ {
		if out, err := runExternalServerBD(t, w.repoDir, w.env, "create", "seed issue", "--json"); err != nil {
			t.Fatalf("bd create in %s failed: %v\n%s", w.db, err, out)
		}
	}
	return w
}

// otherDB names a second database for the test and drops it afterwards.
func (w repointWorkspace) otherDB(t *testing.T, suffix string) string {
	t.Helper()
	name := w.db + "_" + suffix
	t.Cleanup(func() { dropTestDatabase(name, testDoltServerPort) })
	return name
}

// reinit runs `bd init <args>` in the workspace with env and returns its
// combined output and exit code.
func (w repointWorkspace) reinit(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"init", "--prefix", "ext", "--skip-hooks", "--skip-agents"}, args...)
	out, err := runExternalServerBD(t, w.repoDir, env, full...)
	if err == nil {
		return out, 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("bd %s did not run: %v\n%s", strings.Join(full, " "), err, out)
	}
	return out, exitErr.ExitCode()
}

// recordedDB is the raw database name metadata.json records.
func recordedDB(t *testing.T, beadsDir string) string {
	t.Helper()
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load %s metadata: cfg=%v err=%v", beadsDir, cfg, err)
	}
	return cfg.DoltDatabase
}

func requireDatabaseAbsent(t *testing.T, name string) {
	t.Helper()
	check := checkDatabaseOnServer("127.0.0.1", testDoltServerPort, "root", "", name, false)
	if !check.Reachable || check.Err != nil {
		t.Fatalf("test server unreachable checking %s: %+v", name, check)
	}
	if check.Exists {
		t.Fatalf("database %s was created on the server; init must not open its target before refusing", name)
	}
}

// AC-1: an exported BEADS_DOLT_SERVER_DATABASE naming a database the server
// does not have used to make the destroy count read 0 (the count errored), so
// --reinit-local --recreate-missing --database B repointed a populated
// workspace with no token.
func TestInitRepoint_EnvDatabaseCannotMaskConfigured(t *testing.T) {
	w := newRepointWorkspace(t, 1)
	target := w.otherDB(t, "b")
	missing := w.otherDB(t, "missing")
	env := append(append([]string{}, w.env...), "BEADS_DOLT_SERVER_DATABASE="+missing)

	out, code := w.reinit(t, env, "--reinit-local", "--recreate-missing", "--database", target, "--non-interactive")
	if code != ExitDestroyTokenMissing {
		t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
	}
	if !strings.Contains(out, "Existing issues: 1") {
		t.Fatalf("expected the count read from %s (1 issue) in the output:\n%s", w.db, out)
	}
	for _, want := range []string{"Re-initializing will repoint this workspace from database " + w.db + " ", "to " + target + " ",
		"The issues in " + w.db + " will no longer be reachable from this workspace."} {
		if !strings.Contains(out, want) {
			t.Fatalf("the repoint warning does not contain %q:\n%s", want, out)
		}
	}
	requireDatabaseAbsent(t, target)
	requireDatabaseAbsent(t, missing)
	if got := recordedDB(t, w.beadsDir); got != w.db {
		t.Fatalf("metadata.json records %q, want %q", got, w.db)
	}
}

// AC-2: a --server-port or --server-host naming another endpoint used to be
// promoted into the environment before the missing-database guard ran, so the
// guard witnessed the workspace's database at init's target endpoint, found
// nothing there, and refused a populated workspace as "missing". The guard and
// the count read the configured endpoint; the populated database there needs
// the destroy confirmation.
func TestInitRepoint_FlaggedEndpointCountsConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"server-port", []string{"--server-port", "1"}},
		{"server-host", []string{"--server-host", "repoint-target.invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRepointWorkspace(t, 1)
			args := append([]string{"--reinit-local", "--non-interactive"}, tc.args...)
			out, code := w.reinit(t, w.env, args...)
			if code != ExitDestroyTokenMissing {
				t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
			}
			if !strings.Contains(out, "Existing issues: 1") {
				t.Fatalf("expected the count read from %s at its configured endpoint (1 issue):\n%s", w.db, out)
			}
			cfg, err := configfile.LoadForDiscovery(w.beadsDir)
			if err != nil || cfg == nil {
				t.Fatalf("load metadata: %v", err)
			}
			if cfg.DoltDatabase != w.db || cfg.DoltServerPort != testDoltServerPort || cfg.DoltServerHost != "127.0.0.1" {
				t.Fatalf("metadata.json changed: database %q host %q port %d, want %q 127.0.0.1 %d",
					cfg.DoltDatabase, cfg.DoltServerHost, cfg.DoltServerPort, w.db, testDoltServerPort)
			}
		})
	}
}

// newOwnedRepointWorkspace is a workspace on its own bd-managed server (bd
// init --server), seeded with one issue. Its port is bd's port-file
// bookkeeping, which auto-start may replace, and stopping its server leaves
// the port unknown: states the --external fixture, whose port metadata.json
// records, never reaches. Its commands start its server, as they do outside
// tests.
func newOwnedRepointWorkspace(t *testing.T) repointWorkspace {
	t.Helper()
	skipIfNoDolt(t)
	home := t.TempDir()
	w := repointWorkspace{repoDir: t.TempDir(), db: uniqueTestDBName(t), env: append(envWithoutBeadsStorageSettings(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BD_DISABLE_METRICS=1",
		"BEADS_TEST_MODE=",
	)}
	w.beadsDir = filepath.Join(w.repoDir, ".beads")
	initGitRepo(t, w.repoDir)
	stopDoltServerCleanup(t, w.beadsDir)
	// Where a count that missed the workspace's own server let init create it.
	t.Cleanup(func() { dropTestDatabase(w.db, testDoltServerPort) })
	args := []string{"init", "--server", "--quiet", "--database", w.db, "--prefix", "ext", "--skip-hooks", "--skip-agents"}
	if out, err := runExternalServerBD(t, w.repoDir, w.env, args...); err != nil {
		t.Fatalf("bd init --server of %s failed: %v\n%s", w.db, err, out)
	}
	if out, err := runExternalServerBD(t, w.repoDir, w.env, "create", "seed issue", "--json"); err != nil {
		t.Fatalf("bd create in %s failed: %v\n%s", w.db, err, out)
	}
	return w
}

// stopOwnedServer stops the workspace's own server the way an operator does.
func (w repointWorkspace) stopOwnedServer(t *testing.T) {
	t.Helper()
	if out, err := runExternalServerBD(t, w.repoDir, w.env, "dolt", "stop"); err != nil {
		t.Fatalf("bd dolt stop failed: %v\n%s", err, out)
	}
}

// An owned server that dies uncleanly (a crash, an OOM kill, a reboot)
// leaves its pid file naming a process that is gone and its port file naming
// a port where nothing listens. The workspace's commands restart it on a new
// port, and so does the count. It used to hold the stale port as asserted
// and could not open the database, so an in-place reinit skipped its destroy
// confirmation and a repoint was refused as unreadable.
func TestInitRepoint_CrashedOwnedServerIsCounted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		database bool // --database names another database
		warning  string
	}{
		{name: "in place", warning: "Re-initializing will destroy the existing database."},
		{name: "repoint", database: true, warning: "Re-initializing will repoint this workspace from database "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOwnedRepointWorkspace(t)
			port, err := os.ReadFile(filepath.Join(w.beadsDir, doltserver.PortFileName))
			if err != nil {
				t.Fatalf("the owned server wrote no port file: %v", err)
			}
			w.stopOwnedServer(t)
			// PID 99999999 is above any pid_max, so it names no live process.
			writeFile(t, filepath.Join(w.beadsDir, doltserver.PIDFileName), []byte("99999999"))
			writeFile(t, filepath.Join(w.beadsDir, doltserver.PortFileName), port)

			args := []string{"--reinit-local", "--non-interactive"}
			if tc.database {
				args = append(args, "--database", w.db+"_b")
			}
			out, code := w.reinit(t, w.env, args...)
			if code != ExitDestroyTokenMissing {
				t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
			}
			for _, want := range []string{tc.warning, "Existing issues: 1"} {
				if !strings.Contains(out, want) {
					t.Fatalf("expected %q over the count read from %s (1 issue):\n%s", want, w.db, out)
				}
			}
			if got := recordedDB(t, w.beadsDir); got != w.db {
				t.Fatalf("metadata.json records %q, want %q", got, w.db)
			}
		})
	}
}

// bd dolt stop removes the owned server's port file, so its port is unknown
// until the server starts again. No --server-port restates an unknown port:
// the flag names another server, and the count opens the workspace's
// database as its commands would, starting its server. The flag's port used
// to stand in for the unknown one, so the count looked for the database on
// the flagged server, found nothing, and init created an empty database
// there, stranding the workspace's issues.
func TestInitRepoint_StoppedOwnedServerPortFlagRepoints(t *testing.T) {
	w := newOwnedRepointWorkspace(t)
	w.stopOwnedServer(t)

	out, code := w.reinit(t, w.env, "--reinit-local", "--non-interactive", "--server-port", strconv.Itoa(testDoltServerPort))
	if code != ExitDestroyTokenMissing {
		t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
	}
	warning := fmt.Sprintf("Re-initializing will repoint this workspace from database %s (127.0.0.1) to %s (127.0.0.1:%d).",
		w.db, w.db, testDoltServerPort)
	for _, want := range []string{warning, "Existing issues: 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q over the count read from %s (1 issue):\n%s", want, w.db, out)
		}
	}
	requireDatabaseAbsent(t, w.db)
	cfg, err := configfile.LoadForDiscovery(w.beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata: %v", err)
	}
	if cfg.DoltDatabase != w.db || cfg.DoltServerPort != 0 {
		t.Fatalf("metadata.json changed: database %q port %d, want %q and no port", cfg.DoltDatabase, cfg.DoltServerPort, w.db)
	}
}

// A confirmed repoint off the workspace's own server takes effect. That
// server's port file outranks the port init records in metadata.json, so
// while it ran the workspace's commands kept reading and writing the database
// init had just left, and with the server stopped the count started it.
func TestInitRepoint_LeavingTheOwnedServerTakesEffect(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stopped bool // bd dolt stop before the repoint
	}{
		{name: "running"},
		{name: "stopped", stopped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOwnedRepointWorkspace(t)
			if tc.stopped {
				w.stopOwnedServer(t)
			}
			out, code := w.reinit(t, w.env, "--reinit-local", "--non-interactive",
				"--server-port", strconv.Itoa(testDoltServerPort), "--destroy-token", FormatDestroyToken("ext"))
			if code != 0 {
				t.Fatalf("exit = %d, want 0\n%s", code, out)
			}
			requireNoShadowingOwnedServer(t, w.beadsDir, "after the repoint")

			if listed, err := runExternalServerBD(t, w.repoDir, w.env, "list", "--json"); err != nil || strings.Contains(listed, "seed issue") {
				t.Fatalf("bd list after the repoint failed or still read %s's issues (err %v):\n%s", w.db, err, listed)
			}
			if created, err := runExternalServerBD(t, w.repoDir, w.env, "create", "after the repoint", "--json"); err != nil {
				t.Fatalf("bd create after the repoint failed: %v\n%s", err, created)
			}
			if titles := testServerIssueTitles(t, w.db); !slices.Equal(titles, []string{"after the repoint"}) {
				t.Fatalf("the target database %s holds %q, want only the issue created after the repoint", w.db, titles)
			}
			requireNoShadowingOwnedServer(t, w.beadsDir, "after later commands")
			if !strings.Contains(out, "Stopped this workspace's own Dolt server") {
				t.Fatalf("init did not say it stopped the workspace's own server:\n%s", out)
			}
		})
	}
}

// requireNoShadowingOwnedServer fails when beadsDir's own server runs, or
// when its port file names a port other than the test server's: a later
// command records there the port it reached.
func requireNoShadowingOwnedServer(t *testing.T, beadsDir, when string) {
	t.Helper()
	state, err := doltserver.IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("%s: doltserver.IsRunning: %v", when, err)
	}
	if state.Running {
		t.Fatalf("%s: the workspace's own server still runs (pid %d, port %d)", when, state.PID, state.Port)
	}
	if port := doltserver.ReadPortFile(beadsDir); port != 0 && port != testDoltServerPort {
		t.Fatalf("%s: %s names port %d, which outranks the recorded port %d", when, doltserver.PortFileName, port, testDoltServerPort)
	}
}

// testServerIssueTitles lists the issue titles in database db on the test
// server, read there directly rather than through a workspace.
func testServerIssueTitles(t *testing.T, db string) []string {
	t.Helper()
	conn, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root", Database: db}.String())
	if err != nil {
		t.Fatalf("open %s on the test server: %v", db, err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, "SELECT title FROM issues ORDER BY title")
	if err != nil {
		t.Fatalf("list the issues in %s on the test server: %v", db, err)
	}
	defer rows.Close()
	var titles []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatalf("scan an issue title in %s: %v", db, err)
		}
		titles = append(titles, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list the issues in %s on the test server: %v", db, err)
	}
	return titles
}

// AC-3: a configured database whose Tier-1 directory exists but which cannot
// be read has an unknown issue count. Repointing away from it is refused
// outright: neither --recreate-missing nor a valid destroy token can stand in
// for a count init could not take.
func TestInitRepoint_UnreadableConfiguredRefuses(t *testing.T) {
	w := newRepointWorkspace(t, 0)
	target := w.otherDB(t, "b")
	dropTestDatabase(w.db, testDoltServerPort)
	if err := os.MkdirAll(filepath.Join(w.beadsDir, "dolt", w.db, ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}

	out, code := w.reinit(t, w.env, "--reinit-local", "--recreate-missing", "--database", target,
		"--non-interactive", "--destroy-token", FormatDestroyToken("ext"))
	switch code {
	case 0, ExitRemoteDivergenceRefused, ExitLocalExistsRefused, ExitDestroyTokenMissing:
		t.Fatalf("exit = %d, want a plain refusal (none of 0/10/11/12)\n%s", code, out)
	}
	for _, want := range []string{w.db, target, "could not be read"} {
		if !strings.Contains(out, want) {
			t.Fatalf("refusal does not name %q:\n%s", want, out)
		}
	}
	requireDatabaseAbsent(t, target)
	if got := recordedDB(t, w.beadsDir); got != w.db {
		t.Fatalf("metadata.json records %q, want %q", got, w.db)
	}
}

// AC-4: flags that only restate the recorded identity are not a repoint.
func TestResolveInitIdentity_RestatingFlagsDoNotRepoint(t *testing.T) {
	recorded := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "Alpha"}
	ws := initWorkspace{
		BeadsDir:  "/ws/.beads",
		Recorded:  recorded,
		Server:    true,
		Effective: initEndpoint{Host: "127.0.0.1", Port: 3307},
		Own:       initEndpoint{Host: "127.0.0.1", Port: 3307},
	}
	staleEnv := ws
	staleEnv.Effective = initEndpoint{Host: "127.0.0.1", Port: 4410}

	for _, tc := range []struct {
		name     string
		ws       initWorkspace
		flags    initIdentityFlags
		repoints bool
		port     int // the configured port the count must dial
	}{
		{name: "no flags", ws: ws, port: 3307},
		{name: "case-only database", ws: ws, flags: initIdentityFlags{Database: "alpha"}, port: 3307},
		{name: "localhost for 127.0.0.1", ws: ws, flags: initIdentityFlags{Host: "localhost"}, port: 3307},
		{name: "identical values", ws: ws, flags: initIdentityFlags{Database: "Alpha", Host: "127.0.0.1", Port: 3307}, port: 3307},
		{name: "own port over a stale exported port", ws: staleEnv, flags: initIdentityFlags{Port: 3307}, port: 3307},
		{name: "exported port restated", ws: staleEnv, flags: initIdentityFlags{Port: 4410}, port: 4410},
		{name: "other database", ws: ws, flags: initIdentityFlags{Database: "beta"}, repoints: true, port: 3307},
		{name: "other port", ws: ws, flags: initIdentityFlags{Port: 1}, repoints: true, port: 3307},
		{name: "other host", ws: ws, flags: initIdentityFlags{Host: "db.example"}, repoints: true, port: 3307},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := resolveInitIdentity(tc.ws, tc.flags, true)
			if got := id.Repoints(); got != tc.repoints {
				t.Fatalf("Repoints() = %v, want %v (configured %+v, target %+v)", got, tc.repoints, id.Configured, id.Target)
			}
			if id.Configured.Database != "Alpha" || id.Configured.Port != tc.port {
				t.Fatalf("configured = %+v, want database Alpha at port %d", id.Configured, tc.port)
			}
		})
	}
}

// AC-4: a --server-port equal to the workspace's own port while a different
// BEADS_DOLT_SERVER_PORT is exported (GH#5177) restates the workspace, so the
// count is read at the flagged port, as it was before the identity record.
func TestInitRepoint_OwnPortOverStaleEnvIsNotARepoint(t *testing.T) {
	w := newRepointWorkspace(t, 1)
	env := append(append([]string{}, w.env...), "BEADS_DOLT_SERVER_PORT=1")

	out, code := w.reinit(t, env, "--reinit-local", "--non-interactive",
		"--server-port", strconv.Itoa(testDoltServerPort), "--database", strings.ToUpper(w.db))
	if code != ExitDestroyTokenMissing {
		t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
	}
	if !strings.Contains(out, "Existing issues: 1") {
		t.Fatalf("expected the count read at the flagged port (1 issue):\n%s", out)
	}
	if strings.Contains(out, "Repointing") || strings.Contains(out, "repoint this workspace") {
		t.Fatalf("restating flags printed a repoint notice:\n%s", out)
	}
	if !strings.Contains(out, "Re-initializing will destroy the existing database.") {
		t.Fatalf("a same-identity reinit lost the destroy warning:\n%s", out)
	}
}

// AC-5: a project_id workspace whose configured database is missing from its
// server (no Tier-1 directory either) is ADR-0004's missing-database case for
// that database, whatever --database names; --recreate-missing is what lets
// the repoint proceed, and it says so.
func TestInitRepoint_MissingConfiguredNeedsRecreateMissing(t *testing.T) {
	w := newRepointWorkspace(t, 0)
	target := w.otherDB(t, "b")
	dropTestDatabase(w.db, testDoltServerPort)

	out, code := w.reinit(t, w.env, "--reinit-local", "--database", target, "--non-interactive")
	if code == 0 {
		t.Fatalf("bd init repointed away from a missing configured database without --recreate-missing:\n%s", out)
	}
	wantRefusal := fmt.Sprintf("Database %q not found on server at 127.0.0.1:%d", w.db, testDoltServerPort)
	if !strings.Contains(out, wantRefusal) {
		t.Fatalf("expected ADR-0004's refusal naming %s at its configured endpoint (%q):\n%s", w.db, wantRefusal, out)
	}
	requireDatabaseAbsent(t, target)
	if got := recordedDB(t, w.beadsDir); got != w.db {
		t.Fatalf("metadata.json records %q after the refusal, want %q", got, w.db)
	}

	out, code = w.reinit(t, w.env, "--reinit-local", "--recreate-missing", "--database", target, "--non-interactive")
	if code != 0 {
		t.Fatalf("exit = %d with --recreate-missing, want 0\n%s", code, out)
	}
	wantNotice := fmt.Sprintf("Repointing this workspace from database %s (127.0.0.1:%d) to %s (127.0.0.1:%d).",
		w.db, testDoltServerPort, target, testDoltServerPort)
	if !strings.Contains(out, wantNotice) {
		t.Fatalf("expected the repoint notice %q:\n%s", wantNotice, out)
	}
	if got := recordedDB(t, w.beadsDir); got != target {
		t.Fatalf("metadata.json records %q, want %q", got, target)
	}
}

// AC-6: repointing away from a configured database that exists with zero
// issues needs no destroy confirmation, but is announced exactly once.
func TestInitRepoint_EmptyConfiguredProceedsWithOneNotice(t *testing.T) {
	w := newRepointWorkspace(t, 0)
	target := w.otherDB(t, "b")

	out, code := w.reinit(t, w.env, "--reinit-local", "--database", target, "--non-interactive")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if strings.Contains(out, "Existing issues") || strings.Contains(out, "destroy") {
		t.Fatalf("an empty configured database asked for a destroy confirmation:\n%s", out)
	}
	if n := strings.Count(out, "Repointing this workspace"); n != 1 {
		t.Fatalf("printed %d repoint notices, want exactly 1:\n%s", n, out)
	}
	for _, want := range []string{"from database " + w.db + " ", "to " + target + " "} {
		if !strings.Contains(out, want) {
			t.Fatalf("repoint notice does not contain %q:\n%s", want, out)
		}
	}
	if got := recordedDB(t, w.beadsDir); got != target {
		t.Fatalf("metadata.json records %q, want %q", got, target)
	}
}

// The repoint rules beyond restating flags: an IPv6 loopback is not bd's own
// address, a port flag over an unknown configured port (the workspace's own
// server is stopped) names another server, a mode change compares names
// only, and a workspace that records no name, or no metadata at all, never
// repoints by name.
func TestResolveInitIdentity_RepointRules(t *testing.T) {
	recorded := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "Alpha"}
	unnamed := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer}
	ws := initWorkspace{
		BeadsDir:  "/ws/.beads",
		Recorded:  recorded,
		Server:    true,
		Effective: initEndpoint{Host: "127.0.0.1", Port: 3307},
		Own:       initEndpoint{Host: "127.0.0.1", Port: 3307},
	}
	unknownPort := ws
	unknownPort.Effective.Port, unknownPort.Own.Port = 0, 0
	embedded := ws
	embedded.Server = false
	noName := ws
	noName.Recorded = unnamed
	fresh := ws
	fresh.Recorded = nil

	for _, tc := range []struct {
		name         string
		ws           initWorkspace
		flags        initIdentityFlags
		targetServer bool
		repoints     bool
		port         int // the configured port the count must dial
	}{
		{name: "::1 is not 127.0.0.1", ws: ws, flags: initIdentityFlags{Host: "::1"}, targetServer: true, repoints: true, port: 3307},
		{name: "a port flag over an unknown configured port repoints", ws: unknownPort, flags: initIdentityFlags{Port: 4410}, targetServer: true, repoints: true, port: 0},
		{name: "mode change ignores another host", ws: embedded, flags: initIdentityFlags{Host: "db.example"}, targetServer: true, port: 3307},
		{name: "mode change compares names", ws: embedded, flags: initIdentityFlags{Database: "beta"}, targetServer: true, repoints: true, port: 3307},
		{name: "unrecorded name never repoints by name", ws: noName, flags: initIdentityFlags{Database: "beta"}, targetServer: true, port: 3307},
		{name: "fresh workspace never repoints", ws: fresh, flags: initIdentityFlags{Database: "beta", Host: "db.example"}, targetServer: true, port: 3307},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := resolveInitIdentity(tc.ws, tc.flags, tc.targetServer)
			if got := id.Repoints(); got != tc.repoints {
				t.Fatalf("Repoints() = %v, want %v (configured %+v, target %+v)", got, tc.repoints, id.Configured, id.Target)
			}
			if id.Configured.Port != tc.port {
				t.Fatalf("configured = %+v, want port %d", id.Configured, tc.port)
			}
		})
	}
}

// --server-user and --server-tls say how init dials its target. While the
// target is the configured server they are how that server is dialed too, as
// before the identity record; across a move to another server the guards
// dial the configured one as its own commands do, not with the target's user
// or TLS setting. They are promoted only for a server target.
func TestResolveInitIdentity_DialFlagsFollowTheEndpoint(t *testing.T) {
	ws := initWorkspace{
		BeadsDir:  "/ws/.beads",
		Recorded:  &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "alpha"},
		Server:    true,
		Effective: initEndpoint{Host: "127.0.0.1", Port: 3307},
		Own:       initEndpoint{Host: "127.0.0.1", Port: 3307},
		User:      "root",
	}
	tls := true
	dial := initIdentityFlags{User: "admin", TLS: &tls}
	withEndpoint := func(host string, port int) initIdentityFlags {
		flags := dial
		flags.Host, flags.Port = host, port
		return flags
	}

	for _, tc := range []struct {
		name         string
		flags        initIdentityFlags
		targetServer bool
		user         string
		tls          bool
	}{
		{name: "same endpoint", flags: dial, targetServer: true, user: "admin", tls: true},
		{name: "endpoint restated", flags: withEndpoint("localhost", 3307), targetServer: true, user: "admin", tls: true},
		{name: "another host", flags: withEndpoint("db.example", 0), targetServer: true, user: "root"},
		{name: "another port", flags: withEndpoint("", 4410), targetServer: true, user: "root"},
		{name: "embedded target", flags: dial, user: "root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := resolveInitIdentity(ws, tc.flags, tc.targetServer)
			if id.Configured.User != tc.user || id.Configured.TLS != tc.tls {
				t.Fatalf("configured dials as %q tls=%v, want %q tls=%v (configured %+v, target %+v)",
					id.Configured.User, id.Configured.TLS, tc.user, tc.tls, id.Configured, id.Target)
			}
			if tc.targetServer && (id.Target.User != "admin" || !id.Target.TLS) {
				t.Fatalf("target = %+v, want it dialed as admin with TLS", id.Target)
			}
		})
	}
}

// The count opens the configured database as the workspace's commands do.
// Its port keeps the source it was resolved from, so auto-start may replace a
// stale port-file port, unless a flag restates the port and so asserts it.
// Across a move to another server the count dials from the environment the
// workspace's commands use; otherwise init's own flags reach both sides.
func TestResolveInitIdentity_CountOpensAsTheWorkspace(t *testing.T) {
	ws := initWorkspace{
		BeadsDir:  "/ws/.beads",
		Recorded:  &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "alpha"},
		Server:    true,
		Effective: initEndpoint{Host: "127.0.0.1", Port: 3307, PortSource: doltserver.PortSourcePortFile},
		Own:       initEndpoint{Host: "127.0.0.1", Port: 3307, PortSource: doltserver.PortSourcePortFile},
		ConnEnv:   initConnEnv{},
	}
	stopped := ws
	stopped.Effective, stopped.Own = initEndpoint{Host: "127.0.0.1"}, initEndpoint{Host: "127.0.0.1"}

	for _, tc := range []struct {
		name   string
		ws     initWorkspace
		flags  initIdentityFlags
		source doltserver.PortSource // the configured port's, as the count opens it
		ownEnv bool                  // the count dials from the workspace's environment
	}{
		{name: "no flags", ws: ws, source: doltserver.PortSourcePortFile},
		{name: "another database", ws: ws, flags: initIdentityFlags{Database: "beta"}, source: doltserver.PortSourcePortFile},
		{name: "port restated", ws: ws, flags: initIdentityFlags{Port: 3307}, source: doltserver.PortSourceCallerExplicit},
		{name: "another port", ws: ws, flags: initIdentityFlags{Port: 4410}, source: doltserver.PortSourcePortFile, ownEnv: true},
		{name: "another host", ws: ws, flags: initIdentityFlags{Host: "db.example"}, source: doltserver.PortSourcePortFile, ownEnv: true},
		{name: "a port flag over a stopped server", ws: stopped, flags: initIdentityFlags{Port: 4410}, ownEnv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := resolveInitIdentity(tc.ws, tc.flags, true)
			if id.Configured.PortSource != tc.source {
				t.Fatalf("configured port source = %q, want %q (configured %+v)", id.Configured.PortSource, tc.source, id.Configured)
			}
			if got := id.configuredEnv != nil; got != tc.ownEnv {
				t.Fatalf("count dials from the workspace's environment = %v, want %v (configured %+v, target %+v)",
					got, tc.ownEnv, id.Configured, id.Target)
			}
		})
	}
}

// init stops the workspace's own server only for a repoint that moves to
// another server from one the workspace's commands dial by bd's port file,
// or from one that is stopped. It never stops the shared server, a server a
// setting asserts the port of, or the server a repoint stays on.
func TestResolveInitIdentity_LeavesOwnServer(t *testing.T) {
	own := initWorkspace{
		BeadsDir:  "/ws/.beads",
		Recorded:  &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "alpha"},
		Server:    true,
		Effective: initEndpoint{Host: "127.0.0.1", Port: 3307, PortSource: doltserver.PortSourcePortFile},
		Own:       initEndpoint{Host: "127.0.0.1", Port: 3307, PortSource: doltserver.PortSourcePortFile},
	}
	stopped := own
	stopped.Effective, stopped.Own = initEndpoint{Host: "127.0.0.1"}, initEndpoint{Host: "127.0.0.1"}
	shared := own
	shared.Effective = initEndpoint{Host: "127.0.0.1", Port: 3308, PortSource: doltserver.PortSourceSharedServerDefault, PortShared: true}
	shared.Own = shared.Effective
	asserted := own
	asserted.Effective.PortSource, asserted.Own.PortSource = doltserver.PortSourceEnv, doltserver.PortSourceEnv
	fresh := own
	fresh.Recorded = nil

	for _, tc := range []struct {
		name         string
		ws           initWorkspace
		flags        initIdentityFlags
		targetServer bool
		leaves       bool
	}{
		{name: "another port", ws: own, flags: initIdentityFlags{Port: 4410}, targetServer: true, leaves: true},
		{name: "another host", ws: own, flags: initIdentityFlags{Host: "db.example"}, targetServer: true, leaves: true},
		{name: "a port flag over a stopped server", ws: stopped, flags: initIdentityFlags{Port: 4410}, targetServer: true, leaves: true},
		{name: "port restated", ws: own, flags: initIdentityFlags{Port: 3307}, targetServer: true},
		{name: "another database on the same server", ws: own, flags: initIdentityFlags{Database: "beta"}, targetServer: true},
		{name: "embedded target", ws: own, flags: initIdentityFlags{Database: "beta", Port: 4410}},
		{name: "shared server", ws: shared, flags: initIdentityFlags{Port: 4410}, targetServer: true},
		{name: "asserted port", ws: asserted, flags: initIdentityFlags{Port: 4410}, targetServer: true},
		{name: "fresh workspace", ws: fresh, flags: initIdentityFlags{Port: 4410}, targetServer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := resolveInitIdentity(tc.ws, tc.flags, tc.targetServer)
			if got := id.leavesOwnServer(); got != tc.leaves {
				t.Fatalf("leavesOwnServer() = %v, want %v (configured %+v, target %+v)", got, tc.leaves, id.Configured, id.Target)
			}
		})
	}
}

// The count's environment swap restores every variable init's connection
// flags are promoted into: one it missed would reach the configured server
// across a move.
func TestInitConnEnvKeysCoverThePromotedFlags(t *testing.T) {
	c := newServerFlagsCmd()
	for _, flag := range [][2]string{
		{"server-host", "db.example"}, {"server-port", "4410"}, {"server-user", "admin"},
		{"server-socket", "/tmp/dolt.sock"}, {"server-tls", "true"},
	} {
		mustSetFlag(t, c, flag[0], flag[1])
	}
	muts, err := resolveExplicitServerConnEnv(c)
	if err != nil {
		t.Fatal(err)
	}
	var exported []string
	for _, mut := range muts {
		exported = append(exported, mut.key)
	}
	slices.Sort(exported)
	if restored := slices.Sorted(slices.Values(initConnEnvKeys)); !slices.Equal(exported, restored) {
		t.Fatalf("promoteExplicitServerConnFlags exports %v, initConnEnvKeys restores %v", exported, restored)
	}
}

// Plain `bd init` (no --reinit-local) has only the existing-data guard
// between it and a repoint, so that guard must witness the configured
// database at its configured endpoint too. An exported
// BEADS_DOLT_SERVER_DATABASE naming a database the server does not have, or
// a flagged endpoint where nothing listens, used to make it report the
// configured database missing; its --recreate-missing remedy then repointed
// a populated workspace with no count.
func TestInitRepoint_PlainInitGuardsConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(w repointWorkspace, missing string) []string
		args []string
	}{
		{
			name: "exported database",
			env: func(w repointWorkspace, missing string) []string {
				return append(append([]string{}, w.env...), "BEADS_DOLT_SERVER_DATABASE="+missing)
			},
			args: []string{"--recreate-missing"},
		},
		{
			name: "flagged port",
			env:  func(w repointWorkspace, _ string) []string { return w.env },
			args: []string{"--server-port", "1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRepointWorkspace(t, 1)
			target := w.otherDB(t, "b")
			missing := w.otherDB(t, "missing")

			args := append(append([]string{}, tc.args...), "--database", target, "--non-interactive")
			out, code := w.reinit(t, tc.env(w, missing), args...)
			if code == 0 {
				t.Fatalf("plain bd init repointed a populated workspace:\n%s", out)
			}
			if !strings.Contains(out, "This workspace is already initialized.") ||
				!strings.Contains(out, fmt.Sprintf("dolt server at 127.0.0.1:%d", testDoltServerPort)) {
				t.Fatalf("expected the already-initialized refusal naming %s's endpoint 127.0.0.1:%d:\n%s", w.db, testDoltServerPort, out)
			}
			if strings.Contains(out, "not found on server") {
				t.Fatalf("the guard reported the configured database missing:\n%s", out)
			}
			requireDatabaseAbsent(t, target)
			requireDatabaseAbsent(t, missing)
			if got := recordedDB(t, w.beadsDir); got != w.db {
				t.Fatalf("metadata.json records %q, want %q", got, w.db)
			}
		})
	}
}

// The witnesses ask the configured server about the configured database with
// the credentials kept for that server. The password for the server a
// --server-host flag names used to be presented to the configured one, so
// with a credentials file holding an entry for the target only, the witness
// could not log in and refused a populated workspace as missing.
func TestInitRepoint_WitnessesUseTheConfiguredEndpointsCredentials(t *testing.T) {
	const targetHost = "repoint-target.invalid"
	for _, tc := range []struct {
		name   string
		reinit bool
		want   string
	}{
		{name: "reinit counts", reinit: true, want: "Existing issues: 1"},
		{name: "plain init refuses as initialized", want: "This workspace is already initialized."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRepointWorkspace(t, 1)
			credentials := filepath.Join(t.TempDir(), "credentials")
			entry := fmt.Sprintf("[%s:%d]\npassword=only-the-target-server-takes-this\n", targetHost, testDoltServerPort)
			if err := os.WriteFile(credentials, []byte(entry), 0o600); err != nil {
				t.Fatal(err)
			}
			env := append(append([]string{}, w.env...), "BEADS_CREDENTIALS_FILE="+credentials)

			args := []string{"--reinit-local"}
			if !tc.reinit {
				args = []string{"--database", w.otherDB(t, "b")}
			}
			args = append(args, "--server-host", targetHost, "--non-interactive")
			out, code := w.reinit(t, env, args...)
			if tc.reinit && code != ExitDestroyTokenMissing {
				t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
			}
			if code == 0 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit = %d, want a refusal with %q in the output\n%s", code, tc.want, out)
			}
			if strings.Contains(out, "not found on server") {
				t.Fatalf("the witness reported the configured database missing:\n%s", out)
			}
			if got := recordedDB(t, w.beadsDir); got != w.db {
				t.Fatalf("metadata.json records %q, want %q", got, w.db)
			}
		})
	}
}

// --server-user and --server-tls say how init dials the server it moves to.
// The count used to open the configured database with them too, could not
// log in, and refused the repoint as unreadable, which no destroy token
// lifts. It opens the configured database as the workspace's commands do.
func TestInitRepoint_TargetDialFlagsDoNotReachTheCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag []string
	}{
		{"server-user", []string{"--server-user", "target_only_user"}},
		{"server-tls", []string{"--server-tls"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRepointWorkspace(t, 1)
			args := append([]string{"--reinit-local", "--non-interactive", "--server-host", "repoint-target.invalid"}, tc.flag...)
			out, code := w.reinit(t, w.env, args...)
			if code != ExitDestroyTokenMissing {
				t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
			}
			if !strings.Contains(out, "Existing issues: 1") {
				t.Fatalf("expected the count read from %s at its configured endpoint (1 issue):\n%s", w.db, out)
			}
			if got := recordedDB(t, w.beadsDir); got != w.db {
				t.Fatalf("metadata.json records %q, want %q", got, w.db)
			}
		})
	}
}

// AC-3's other half: without a repoint, a configured database that exists but
// cannot be read keeps the historical skip, so `bd init --reinit-local` can
// still replace a corrupt database in place.
func TestInitRepoint_SameIdentityUnreadableKeepsTheSkip(t *testing.T) {
	w := newRepointWorkspace(t, 0)
	dropTestDatabase(w.db, testDoltServerPort)
	if err := os.MkdirAll(filepath.Join(w.beadsDir, "dolt", w.db, ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}

	out, code := w.reinit(t, w.env, "--reinit-local", "--non-interactive")
	if strings.Contains(out, "refusing to repoint") || strings.Contains(out, "could not be read") {
		t.Fatalf("a same-identity reinit of an unreadable database was refused as a repoint:\n%s", out)
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0: the reinit replaces the database it could not read\n%s", code, out)
	}
	if got := recordedDB(t, w.beadsDir); got != w.db {
		t.Fatalf("metadata.json records %q, want %q", got, w.db)
	}
}

// countOnlyStore is what a registered backend's read-only open hands the
// count: only GetStatistics and Close are reached.
type countOnlyStore struct {
	storage.DoltStorage
	issues int
}

func (s countOnlyStore) GetStatistics(context.Context) (*types.Statistics, error) {
	return &types.Statistics{TotalIssues: s.issues}, nil
}

func (countOnlyStore) Close() error { return nil }

// A workspace on a registered backend records no dolt_mode and has no
// embedded directory, so it is neither Dolt shape the count probes. Its
// issues are counted through the backend's own read-only open, and a
// populated workspace needs the destroy confirmation, as it did when the
// count went through the backend-aware factory.
func TestInitReinitPreflight_RegisteredBackendCountsItsIssues(t *testing.T) {
	const name = "init-count-registered"
	var opened []string
	backends.Register(name, backends.Backend{
		Open: func(context.Context, string) (storage.DoltStorage, error) {
			opened = append(opened, "read-write")
			return nil, errors.New("the count must not open the backend read-write")
		},
		OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
			opened = append(opened, "read-only")
			return countOnlyStore{issues: 1}, nil
		},
		WorkspaceIsBeadsDir: true,
	})
	t.Cleanup(func() { backends.Deregister(name) })
	beadsDir := writeContractBackendConfig(t, name)
	metadata, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	id := resolveInitIdentity(resolveInitWorkspace(), initIdentityFlags{}, false)

	err = runInitReinitPreflight(true, "ext", "", id)
	var exitErr *exitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDestroyTokenMissing {
		t.Fatalf("preflight = %v, want exit %d: one issue needs the destroy token", err, ExitDestroyTokenMissing)
	}
	if err := runInitReinitPreflight(true, "ext", FormatDestroyToken("ext"), id); err != nil {
		t.Fatalf("preflight with the destroy token = %v, want nil", err)
	}
	if want := []string{"read-only", "read-only"}; !slices.Equal(opened, want) {
		t.Fatalf("backend opens = %v, want %v", opened, want)
	}
	if after, err := os.ReadFile(configfile.ConfigPath(beadsDir)); err != nil || !bytes.Equal(after, metadata) {
		t.Fatalf("metadata.json changed (err %v):\n%s", err, after)
	}
}

// init resolves its identity before it waits for the init gate. When another
// init repoints the workspace meanwhile, the preflight under the gate refuses
// rather than count a database the write path will not find.
func TestInitReinitPreflight_RefusesARecordChangedWhileWaiting(t *testing.T) {
	beadsDir := t.TempDir()
	record := func(database string) {
		t.Helper()
		cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeEmbedded, DoltDatabase: database}
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatal(err)
		}
	}
	record("alpha")
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	id := resolveInitIdentity(resolveInitWorkspace(), initIdentityFlags{}, false)

	if err := runInitReinitPreflight(true, "ext", "", id); err != nil {
		t.Fatalf("preflight over an unchanged record = %v, want nil (no embedded database to count)", err)
	}
	record("gamma")
	err := runInitReinitPreflight(true, "ext", "", id)
	if err == nil || !strings.Contains(err.Error(), "changed while this init waited") {
		t.Fatalf("preflight after another init repointed the workspace = %v, want the changed-record refusal", err)
	}
}
