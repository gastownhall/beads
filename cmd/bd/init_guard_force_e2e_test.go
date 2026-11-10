//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestInitGuard_ForceE2EDoesNotRecreateMissingServerDB is the behavioural proof
// for review item 1 on PR #5791. The unit subtests above pin
// guardMissingServerDatabaseAt's semantics; only this one proves the reinit
// path actually CALLS it, which is where the bug lived — the guard existed and
// was simply never reached with --force.
//
// Runs the real binary, because that is the only way to exercise the
// --force -> reinitLocal -> "skip checkExistingBeadsData" path end to end.
// Without the wiring this test fails: init proceeds past the guard and reports
// a connection failure instead of the refusal.
func TestInitGuard_ForceE2EDoesNotRecreateMissingServerDB(t *testing.T) {
	bd := buildBDUnderTest(t)

	for _, flag := range []string{"--force", "--reinit-local"} {
		t.Run(flag, func(t *testing.T) {
			projectDir := t.TempDir()
			beadsDir := filepath.Join(projectDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}
			metadata := map[string]interface{}{
				"database":      "dolt",
				"backend":       "dolt",
				"dolt_mode":     "server",
				"dolt_database": "myproject",
				"project_id":    "existing-0000-1111-2222-333344445555",
			}
			data, _ := json.Marshal(metadata)
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
				t.Fatal(err)
			}

			// Port 1 is unreachable by construction, standing in for a server
			// whose database has been lost.
			// #nosec G204 -- bd is a locally built binary, flag is a literal
			cmd := exec.Command(bd, "init", flag, "--prefix", "myproject", "--skip-hooks")
			cmd.Dir = projectDir
			cmd.Env = initGuardE2EEnv(t, 1)
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Fatalf("bd init %s against a missing server-side database must fail, got success.\nOutput:\n%s", flag, out)
			}
			if !strings.Contains(string(out), "not found on server") {
				t.Fatalf("bd init %s must be refused by the missing-database guard, not fail incidentally.\nWanted the guard's refusal; got:\n%s", flag, out)
			}
			if !strings.Contains(string(out), "--recreate-missing") {
				t.Errorf("refusal must name the explicit opt-in flag, got:\n%s", out)
			}
		})
	}
}

// TestInitGuard_RecreateMissingE2ECreatesMissingServerDB is the success-side
// twin of the test above. The unit tests that permit --recreate-missing flip
// initAllowRecreateMissing directly, so nothing else runs the documented
// recovery command itself through what follows the guard (remote safety, the
// metadata rewrite, CREATE DATABASE). This runs the real binary against a live
// server that lacks the configured database: refused without the opt-in, and
// with it, init succeeds and the database exists on that server.
func TestInitGuard_RecreateMissingE2ECreatesMissingServerDB(t *testing.T) {
	beadsDir := startProjectServerModeGuardFixture(t, "myproject")
	port, err := strconv.Atoi(os.Getenv("BEADS_DOLT_SERVER_PORT"))
	if err != nil {
		t.Fatalf("fixture must pin BEADS_DOLT_SERVER_PORT: %v", err)
	}
	// The legacy-upgrade guard runs before init's own guards and refuses a
	// server workspace whose local Dolt storage has no current-era version
	// witness; a workspace bd initialized has one.
	if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), Version); err != nil {
		t.Fatalf("write local version: %v", err)
	}
	if check := checkDatabaseOnServer("127.0.0.1", port, "root", "", "myproject", false); !check.Reachable || check.Exists {
		t.Fatalf("precondition: server must be reachable and lack the database, got %+v", check)
	}

	projectDir := filepath.Dir(beadsDir)
	env := initGuardE2EEnv(t, port)
	args := []string{"init", "--prefix", "myproject", "--skip-hooks", "--skip-agents"}

	// Without the opt-in this exact workspace is refused, so the success below
	// is the flag's doing rather than a fixture init would accept anyway.
	out, err := runExternalServerBD(t, projectDir, env, args...)
	if err == nil || !strings.Contains(out, "not found on server") {
		t.Fatalf("precondition: bd init without --recreate-missing must be refused by the missing-database guard, got err=%v:\n%s", err, out)
	}

	out, err = runExternalServerBD(t, projectDir, env, append(args, "--recreate-missing")...)
	if err != nil {
		t.Fatalf("bd init --recreate-missing must create the missing database, got %v:\n%s", err, out)
	}
	if check := checkDatabaseOnServer("127.0.0.1", port, "root", "", "myproject", false); !check.Reachable || !check.Exists {
		t.Fatalf("bd init --recreate-missing reported success but the database is not on the server: %+v\n%s", check, out)
	}
}

// initGuardE2EEnv builds a bd child environment in which only the fixture's
// files and the given port choose the server. Every BEADS_DOLT_* setting
// (shared-server mode, data dir, mode, the TestMain container's port) and the
// BEADS_DIR, BEADS_DB, BD_DB and BEADS_SHARED_SERVER_DIR overrides are
// dropped, which is what startProjectServerModeGuardFixture pins in-process.
// The mode variables are dropped rather than set to "0":
// initModeExplicitlyRequested treats any non-empty value as an explicit mode
// choice, and init would then stop inheriting server mode from metadata.json.
func initGuardE2EEnv(t *testing.T, port int) []string {
	t.Helper()
	env := []string{}
	for _, kv := range externalServerTestEnv(t) {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "BEADS_DB", "BD_DB", "BEADS_SHARED_SERVER_DIR":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port),
		"BD_NON_INTERACTIVE=1",
	)
}
