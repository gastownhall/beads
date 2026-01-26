//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func runBDExecAllowErrorWithEnv(t *testing.T, dir string, extraEnv []string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command(testBD, args...)
	cmd.Dir = dir

	// Start from a clean-ish environment, then apply overrides.
	// NOTE: we keep os.Environ() so PATH etc still work for git/dolt.
	env := append([]string{}, os.Environ()...)
	env = append(env, extraEnv...)
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDoltDaemonAutostart_NoTimeoutOnCreate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt daemon integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)

	// Set up a real git repo so daemon autostart is allowed.
	if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test User")

	socketPath := filepath.Join(tmpDir, ".beads", "bd.sock")
	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_AUTO_START_DAEMON=true",
		"BEADS_NO_DAEMON=0",
		"BD_SOCKET=" + socketPath,
	}

	// Init dolt backend.
	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		// If dolt backend isn't available in this build, skip rather than fail.
		// (Some environments may build without dolt support.)
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	// Always stop daemon on cleanup (best effort) so temp dir can be removed.
	t.Cleanup(func() {
		_, _ = runBDExecAllowErrorWithEnv(t, tmpDir, env, "daemon", "stop")
		// Give the daemon a moment to release any locks/files.
		time.Sleep(200 * time.Millisecond)
	})

	// Create should auto-start daemon and should NOT fall back with a timeout warning.
	createOut, createErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "create", "dolt daemon autostart test", "--json")
	if createErr != nil {
		t.Fatalf("bd create failed: %v\n%s", createErr, createOut)
	}
	if strings.Contains(createOut, "Daemon took too long to start") || strings.Contains(createOut, "Running in direct mode") {
		t.Fatalf("unexpected daemon fallback on dolt create; output:\n%s", createOut)
	}

	// Verify daemon reports running (via JSON output).
	statusOut, statusErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "daemon", "status", "--json")
	if statusErr != nil {
		t.Fatalf("bd daemon status failed: %v\n%s", statusErr, statusOut)
	}

	// We accept either the legacy DaemonStatusResponse shape (daemon_lifecycle.go)
	// or the newer DaemonStatusReport shape (daemon_status.go), depending on flags/routes.
	// Here we just assert it isn't obviously "not_running".
	var m map[string]any
	if err := json.Unmarshal([]byte(statusOut), &m); err != nil {
		// Sometimes status may print warnings before JSON; try from first '{'.
		if idx := strings.Index(statusOut, "{"); idx >= 0 {
			if err2 := json.Unmarshal([]byte(statusOut[idx:]), &m); err2 != nil {
				t.Fatalf("failed to parse daemon status JSON: %v\n%s", err2, statusOut)
			}
		} else {
			t.Fatalf("failed to parse daemon status JSON: %v\n%s", err, statusOut)
		}
	}

	// Check "running" boolean (legacy) or "status" string (new).
	if runningVal, ok := m["running"]; ok {
		if b, ok := runningVal.(bool); ok && !b {
			t.Fatalf("expected daemon running=true, got: %s", statusOut)
		}
	} else if statusVal, ok := m["status"]; ok {
		if s, ok := statusVal.(string); ok && (s == "not_running" || s == "stale" || s == "unresponsive") {
			t.Fatalf("expected daemon to be running/healthy, got status=%q; full: %s", s, statusOut)
		}
	} else {
		// If schema changes again, this will fail loudly and force an update.
		t.Fatalf("unexpected daemon status JSON shape (missing running/status): %s", statusOut)
	}
}
