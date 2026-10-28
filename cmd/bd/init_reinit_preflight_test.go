package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/term"
)

// These tests pin the --reinit-local destroy-confirmation preflight's answer
// to GH#5628: an uncountable workspace must be confirmed, never treated as
// empty, and the count must describe the workspace the gate replaces —
// beadsDirAbs, not whatever BEADS_DIR or the FindBeadsDir walk-up would find.
// All of them are Dolt-free: they drive the count into failure through the
// workspace's metadata itself, never through a real database.

// uncountableTargetBeadsDir returns a .beads whose issues cannot be counted:
// it holds beads project files (an unloadable metadata.json), so the store
// open — and with it the count — fails, which is the shape of a slow, large or
// damaged database the preflight used to read as "nothing to destroy".
func uncountableTargetBeadsDir(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unloadable metadata.json makes the store open (and so the count) fail.
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := countReinitTargetIssues(beadsDir); err == nil {
		t.Fatal("precondition: countReinitTargetIssues should fail on an unloadable metadata.json")
	}
	return beadsDir
}

// TestRunInitReinitPreflight_CountErrorRefuses pins that a count error refuses
// rather than silently skipping the destroy confirmation, in the same shape
// as a counted non-zero workspace: exit 12 (ExitDestroyTokenMissing), the
// target directory named, the count reported as unknown with the count error,
// and no destroy-token value in the output (ADR-0002 Invariant 4).
func TestRunInitReinitPreflight_CountErrorRefuses(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal: the non-interactive refusal this pins would be a typed prompt instead")
	}

	t.Run("refuses without a destroy token, naming the target and the error", func(t *testing.T) {
		beadsDir := uncountableTargetBeadsDir(t)
		isolateBeadsDirForTest(t)

		var err error
		stderr := captureStderr(t, func() {
			err = runInitReinitPreflight(true, beadsDir, "bd", "")
		})
		if code, ok := exitCodeFromError(err); !ok || code != ExitDestroyTokenMissing {
			t.Fatalf("runInitReinitPreflight = %v, want exit %d (ExitDestroyTokenMissing): the count error silently skipped the destroy confirmation", err, ExitDestroyTokenMissing)
		}
		if !strings.Contains(stderr, "Existing issues: unknown (could not count them:") {
			t.Errorf("stderr must report the count as unknown with the count error, got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Target: "+beadsDir) {
			t.Errorf("stderr must name the target .beads directory, got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "Refusing to destroy an unknown number of issues in non-interactive mode.") {
			t.Errorf("stderr must refuse in the non-interactive shape, got:\n%s", stderr)
		}
		if strings.Contains(stderr, "DESTROY-") {
			t.Errorf("stderr must not echo the destroy token value (ADR-0002 Invariant 4), got:\n%s", stderr)
		}
	})

	t.Run("counts the gate's target, not BEADS_DIR", func(t *testing.T) {
		beadsDir := uncountableTargetBeadsDir(t)
		// BEADS_DIR names a directory that counts as zero (no project
		// files). The pre-fixed preflight resolved its workspace through
		// BEADS_DIR, so this row used to pass with no confirmation at all.
		// The refusal must be for the gate's target: the argument.
		otherDir := filepath.Join(t.TempDir(), "other", "beads")
		if err := os.MkdirAll(otherDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(otherDir, "registry.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", otherDir)

		var err error
		stderr := captureStderr(t, func() {
			err = runInitReinitPreflight(true, beadsDir, "bd", "")
		})
		if code, ok := exitCodeFromError(err); !ok || code != ExitDestroyTokenMissing {
			t.Fatalf("runInitReinitPreflight = %v, want exit %d (ExitDestroyTokenMissing) for the gate's target, not BEADS_DIR's workspace", err, ExitDestroyTokenMissing)
		}
		if !strings.Contains(stderr, "Target: "+beadsDir) {
			t.Errorf("the refusal must name the target passed in, got:\n%s", stderr)
		}
		if strings.Contains(stderr, otherDir) {
			t.Errorf("the refusal must not name the BEADS_DIR workspace, got:\n%s", stderr)
		}
	})
}

// TestCountReinitTargetIssues pins the count's own contract: which workspaces
// count as provably empty (zero, no error), and which must surface their
// errors so the preflight treats them as "may hold data".
func TestCountReinitTargetIssues(t *testing.T) {
	t.Run("an unloadable metadata.json is an error, never zero", func(t *testing.T) {
		beadsDir := uncountableTargetBeadsDir(t)
		count, err := countReinitTargetIssues(beadsDir)
		if err == nil {
			t.Fatalf("countReinitTargetIssues = (%d, nil), want an error: an unloadable store must be unknown, not empty", count)
		}
	})

	t.Run("a server-mode workspace with a local copy is an error, never absent", func(t *testing.T) {
		beadsDir := writeServerModeTargetWorkspace(t, true /* withLocalCopy */)
		count, err := countReinitTargetIssues(beadsDir)
		if err == nil {
			t.Fatalf("countReinitTargetIssues = (%d, nil), want an error: a local copy can hold data, so an unreachable server must not read as absent", count)
		}
	})

	t.Run("a server-mode workspace with an unreachable server is an error, never absent", func(t *testing.T) {
		// No local copy, but the server is not reachable either: only a
		// REACHABLE server reporting the database gone proves absence.
		beadsDir := writeServerModeTargetWorkspace(t, false /* withLocalCopy */)
		count, err := countReinitTargetIssues(beadsDir)
		if err == nil {
			t.Fatalf("countReinitTargetIssues = (%d, nil), want an error: absence is proven by a reachable server, and nothing reaches port 1", count)
		}
	})
}

// writeServerModeTargetWorkspace writes a .beads whose metadata selects Dolt
// server mode against a port nothing listens on: the store open fails, so
// whether the count surfaces that error or rewrites it as "absent" is exactly
// what the caller is pinning. withLocalCopy also lays down a local
// <dolt>/<db>/.dolt directory, which must make "absent" false on its own: a
// local copy can hold data while the server is unreachable.
func writeServerModeTargetWorkspace(t *testing.T, withLocalCopy bool) string {
	t.Helper()
	// Nothing listens at 127.0.0.1:1, and the OS refuses the connect fast;
	// environment wins over any ambient server this host runs (the agent
	// host's own Dolt server would otherwise answer, and the count would
	// succeed or fail for reasons this fixture does not control).
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{
		"database":         "dolt",
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_database":    "myproject",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 1,
		"project_id":       "existing-0000-1111-2222-333344445555",
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if withLocalCopy {
		localDB := filepath.Join(beadsDir, "dolt", "myproject", ".dolt")
		if err := os.MkdirAll(localDB, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return beadsDir
}
