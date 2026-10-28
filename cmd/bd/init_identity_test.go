//go:build cgo

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
