//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
			env := []string{}
			for _, kv := range os.Environ() {
				switch {
				case strings.HasPrefix(kv, "BEADS_DIR="),
					strings.HasPrefix(kv, "BEADS_DB="),
					strings.HasPrefix(kv, "BD_DB="),
					strings.HasPrefix(kv, "BEADS_DOLT_SERVER_PORT="),
					strings.HasPrefix(kv, "BEADS_DOLT_SERVER_DATABASE="):
					continue
				}
				env = append(env, kv)
			}
			env = append(env,
				"BEADS_DOLT_SERVER_PORT=1",
				"BD_NON_INTERACTIVE=1",
			)

			// #nosec G204 -- bd is a locally built binary, flag is a literal
			cmd := exec.Command(bd, "init", flag, "--prefix", "myproject", "--skip-hooks")
			cmd.Dir = projectDir
			cmd.Env = env
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
