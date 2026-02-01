//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDoltNativeNoFallback verifies that when Dolt connection fails in dolt-native
// mode, bd does NOT create SQLite (beads.db) or JSONL (issues.jsonl) as fallback.
//
// This test validates the fix from bd-m2jr: the factory pattern ensures all code
// paths use the configured backend and don't fall back to SQLite on failure.
func TestDoltNativeNoFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt native fallback test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)

	// Set up a real git repo so bd commands behave normally.
	if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test User")

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write metadata.json configuring Dolt in server mode with a bad server.
	// Port 12345 is intentionally invalid/unreachable to simulate connection failure.
	metadata := map[string]interface{}{
		"database":         "dolt",
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 12345, // Unreachable port to trigger connection failure
		"dolt_server_user": "root",
		"dolt_database":    "beads",
	}
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0o600); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	// Write config.yaml with sync.mode=dolt-native.
	configYAML := `# Test config for dolt-native mode
issue-prefix: "test"
sync.mode: "dolt-native"
`
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_NO_DAEMON=1", // Force direct mode to test factory behavior
		"BEADS_DIR=" + beadsDir,
	}

	// Attempt to run a command that requires storage access.
	// This should FAIL because Dolt connection fails, but should NOT create SQLite/JSONL.
	out, cmdErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "list", "--json")

	// We expect an error because the Dolt server is unreachable.
	// The specific error varies by build (with/without CGO), but it should fail.
	if cmdErr == nil {
		// If dolt backend isn't available in this build, the error may be different.
		lower := strings.ToLower(out)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "cgo")) {
			t.Skipf("dolt backend not available in this build: %s", out)
		}
		// If it succeeded, that's unexpected for a bad server config.
		t.Logf("Command output (expected error): %s", out)
		// It might succeed with empty results if no issues exist, but let's check files.
	}

	// Give any async operations time to complete.
	time.Sleep(100 * time.Millisecond)

	// CRITICAL ASSERTION: beads.db must NOT exist.
	// This is the main regression test for bd-m2jr.
	sqliteDB := filepath.Join(beadsDir, "beads.db")
	if _, err := os.Stat(sqliteDB); err == nil {
		t.Fatalf("REGRESSION: SQLite database %s was created as fallback in dolt-native mode", sqliteDB)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking for beads.db: %v", err)
	}

	// NOTE: In dolt-native mode, JSONL is now export-only (backup). However, since
	// Dolt connection failed above, no storage was created and no export could happen.
	// So JSONL should still not exist in this failure case.
	jsonlFile := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlFile); err == nil {
		t.Fatalf("REGRESSION: JSONL file %s was created in dolt-native mode with no storage", jsonlFile)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking for issues.jsonl: %v", err)
	}

	t.Logf("SUCCESS: No SQLite or JSONL fallback created when Dolt connection failed")
}

// TestDoltNativeNoFallback_CreateCommand tests that the create command also
// does not fall back to SQLite when Dolt connection fails.
func TestDoltNativeNoFallback_CreateCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt native fallback test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)

	// Set up a real git repo.
	if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test User")

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write metadata.json with bad Dolt server config.
	metadata := map[string]interface{}{
		"database":         "dolt",
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 12346, // Another unreachable port
		"dolt_server_user": "root",
		"dolt_database":    "beads",
	}
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0o600); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	// Write config.yaml with sync.mode=dolt-native.
	configYAML := `issue-prefix: "test"
sync.mode: "dolt-native"
`
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_NO_DAEMON=1",
		"BEADS_DIR=" + beadsDir,
	}

	// Attempt to create an issue. This should fail without SQLite fallback.
	out, cmdErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "create", "test issue", "--json")

	// Check if dolt not available
	if cmdErr == nil {
		lower := strings.ToLower(out)
		if strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "cgo")) {
			t.Skipf("dolt backend not available in this build: %s", out)
		}
	}

	// The command should have failed (connection refused).
	if cmdErr == nil {
		// Check if it actually created something (which would be a regression).
		t.Logf("create command output: %s", out)
	}

	time.Sleep(100 * time.Millisecond)

	// Verify no SQLite fallback.
	sqliteDB := filepath.Join(beadsDir, "beads.db")
	if _, err := os.Stat(sqliteDB); err == nil {
		t.Fatalf("REGRESSION: SQLite database created by 'create' command in dolt-native mode")
	}

	// NOTE: In dolt-native mode, JSONL is now export-only (backup). However, since
	// Dolt connection failed above, no storage was created and no export could happen.
	jsonlFile := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlFile); err == nil {
		t.Fatalf("REGRESSION: JSONL file created by 'create' command in dolt-native mode with no storage")
	}

	t.Logf("SUCCESS: 'create' command did not fall back to SQLite/JSONL")
}
