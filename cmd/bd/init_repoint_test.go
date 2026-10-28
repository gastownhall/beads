//go:build cgo

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// repointWorkspace is a server-mode workspace bd init created on the test
// server, recording database a. The tests below re-run init against it with
// flags or exported variables that name another database or endpoint, which is
// the #6172 shape: init's guards must reason about a, the database the
// workspace's commands open, not the one init is about to write.
type repointWorkspace struct {
	dir      string
	beadsDir string
	env      []string
	a        string
}

func newRepointWorkspace(t *testing.T, issues int) repointWorkspace {
	t.Helper()
	skipIfNoDolt(t)
	w := repointWorkspace{dir: t.TempDir(), env: externalServerTestEnv(t), a: uniqueTestDBName(t)}
	w.beadsDir = filepath.Join(w.dir, ".beads")
	initGitRepo(t, w.dir)
	t.Cleanup(func() { dropTestDatabase(w.a, testDoltServerPort) })
	if out, err := runExternalServerBD(t, w.dir, w.env, externalServerInitArgs(w.a)...); err != nil {
		t.Fatalf("bd init database %s: %v\n%s", w.a, err, out)
	}
	for i := 0; i < issues; i++ {
		if out, err := runExternalServerBD(t, w.dir, w.env, "create", "--title", "repoint seed", "--type", "task"); err != nil {
			t.Fatalf("bd create: %v\n%s", err, out)
		}
	}
	return w
}

// reinit runs bd init --reinit-local against the workspace with extra env and
// args, returning the output and exit code.
func (w repointWorkspace) reinit(t *testing.T, extraEnv []string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"init", "--reinit-local", "--non-interactive", "--prefix", "ext", "--skip-hooks", "--skip-agents"}, args...)
	out, err := runExternalServerBD(t, w.dir, append(append([]string{}, w.env...), extraEnv...), full...)
	if err == nil {
		return out, 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("bd %s: %v\n%s", strings.Join(full, " "), err, out)
	}
	return out, exitErr.ExitCode()
}

func (w repointWorkspace) recordedDatabase(t *testing.T) string {
	t.Helper()
	cfg, err := configfile.LoadForDiscovery(w.beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json: cfg=%v err=%v", cfg, err)
	}
	return cfg.DoltDatabase
}

func assertNotOnTestServer(t *testing.T, db string) {
	t.Helper()
	check := checkDatabaseOnServer("127.0.0.1", testDoltServerPort, "root", "", db, false)
	if !check.Reachable {
		t.Fatalf("test server unreachable while checking %s: %+v", db, check)
	}
	if check.Exists {
		t.Errorf("database %s was created on the test server", db)
	}
}

// AC-1: an exported BEADS_DOLT_SERVER_DATABASE naming a database the server
// does not have used to make the destroy count fail, and a failed count skips
// the confirmation, so `--reinit-local --database B` repointed a workspace
// whose database still held issues with no token.
func TestInitRepoint_EnvDatabaseCannotMaskConfigured(t *testing.T) {
	w := newRepointWorkspace(t, 1)
	missing := uniqueTestDBName(t) + "_absent"
	b := uniqueTestDBName(t) + "_b"
	t.Cleanup(func() { dropTestDatabase(b, testDoltServerPort) })

	out, code := w.reinit(t, []string{"BEADS_DOLT_SERVER_DATABASE=" + missing}, "--recreate-missing", "--database", b)
	if code != ExitDestroyTokenMissing {
		t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
	}
	if !strings.Contains(out, "Existing issues: 1") {
		t.Errorf("the count must be read from %s (1 issue):\n%s", w.a, out)
	}
	assertNotOnTestServer(t, b)
	if got := w.recordedDatabase(t); got != w.a {
		t.Errorf("metadata.json dolt_database = %q, want %q", got, w.a)
	}
}

// AC-2: init promotes --server-port/--server-host into BEADS_DOLT_SERVER_*
// before its guards ran, so they and the destroy count dialed the flagged
// endpoint, where the configured database is not. Port 1 is unreachable by
// construction.
func TestInitRepoint_FlaggedEndpointCountsConfigured(t *testing.T) {
	w := newRepointWorkspace(t, 1)

	out, code := w.reinit(t, nil, "--server-port", "1")
	if code != ExitDestroyTokenMissing {
		t.Fatalf("exit = %d, want %d (destroy token missing)\n%s", code, ExitDestroyTokenMissing, out)
	}
	if !strings.Contains(out, "Existing issues: 1") {
		t.Errorf("the count must be read from %s at its configured endpoint (1 issue):\n%s", w.a, out)
	}
	cfg, err := configfile.LoadForDiscovery(w.beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json: cfg=%v err=%v", cfg, err)
	}
	if cfg.DoltDatabase != w.a || cfg.DoltServerPort != testDoltServerPort {
		t.Errorf("metadata.json changed: dolt_database=%q dolt_server_port=%d, want %q and %d",
			cfg.DoltDatabase, cfg.DoltServerPort, w.a, testDoltServerPort)
	}
}
