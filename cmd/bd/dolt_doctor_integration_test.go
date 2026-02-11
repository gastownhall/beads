//go:build cgo && integration

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDoltDoctor_NoSQLiteWarningsAfterInitAndCreate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt doctor integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)

	// Set up a real git repo so init/create/doctor behave normally.
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
		lower := strings.ToLower(initOut)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown")) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	// Ensure daemon cleanup so temp dir removal doesn't flake.
	t.Cleanup(func() {
		_, _ = runBDExecAllowErrorWithEnv(t, tmpDir, env, "daemon", "stop")
		time.Sleep(200 * time.Millisecond)
	})

	// Create one issue so the store is definitely initialized.
	createOut, createErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "create", "doctor dolt smoke", "--json")
	if createErr != nil {
		t.Fatalf("bd create failed: %v\n%s", createErr, createOut)
	}

	// Run doctor; it may return non-zero for unrelated warnings (upstream, claude, etc),
	// but it should NOT include SQLite-only failures on dolt.
	doctorOut, _ := runBDExecAllowErrorWithEnv(t, tmpDir, env, "doctor")

	// Also include stderr-like output if doctor wrote it to stdout in some modes.
	// (CombinedOutput already captures both.)
	for _, forbidden := range []string{
		"No beads.db found",
		"Unable to read database version",
		"Legacy database",
	} {
		if strings.Contains(doctorOut, forbidden) {
			t.Fatalf("bd doctor printed sqlite-specific warning %q in dolt mode; output:\n%s", forbidden, doctorOut)
		}
	}

	// Sanity check: doctor should mention dolt somewhere so we know we exercised the right path.
	if !strings.Contains(strings.ToLower(doctorOut), "dolt") {
		// Some doctor output is terse depending on flags; don't be too strict, but
		// if it's completely missing, that usually means we didn't use dolt config.
		t.Fatalf("bd doctor output did not mention dolt; output:\n%s", doctorOut)
	}

	// Regression check: dolt init must NOT create a SQLite database file.
	if _, err := os.Stat(filepath.Join(tmpDir, ".beads", "beads.db")); err == nil {
		t.Fatalf("unexpected sqlite database created in dolt mode: %s", filepath.Join(tmpDir, ".beads", "beads.db"))
	}
}
