//go:build cgo

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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
	if strings.Contains(out, "Repointing") {
		t.Fatalf("restating flags printed a repoint notice:\n%s", out)
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
