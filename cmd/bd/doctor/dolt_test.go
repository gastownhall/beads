package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDoltHealthChecks_NonDoltBackend was removed: SQLite backend no longer
// exists. GetBackend() always returns "dolt" after the dolt-native cleanup.
// (bd-yqpwy)

func TestRunDoltHealthChecks_DoltBackendNoServer(t *testing.T) {
	// GH#2722: In owned/embedded mode (non-external), when no server is
	// running, server-dependent checks should be skipped gracefully (StatusOK)
	// instead of reporting false errors. The embedded SharedStore checks
	// already cover data integrity.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Write metadata.json marking this as dolt backend (no explicit server port → owned mode)
	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// No BEADS_DOLT_SERVER_PORT set → port 0 → no server running
	// No BEADS_DOLT_SHARED_SERVER → owned mode (not external)
	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 7 {
		t.Fatalf("expected exactly 7 checks (consistent shape), got %d", len(checks))
	}

	// Verify check names are consistent
	expectedNames := []string{"Dolt Connection", "Dolt Schema", "Dolt Issue Count", "Dolt Status", "Dolt Lock Health", "Phantom Databases", "Shared Server"}
	for i, name := range expectedNames {
		if checks[i].Name != name {
			t.Errorf("checks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}

	// Server-dependent checks should be OK (gracefully skipped), not errors
	for _, idx := range []int{0, 1, 2, 3, 5} {
		if checks[idx].Status != StatusOK {
			t.Errorf("checks[%d] (%s): expected StatusOK (graceful skip), got %s: %s",
				idx, checks[idx].Name, checks[idx].Status, checks[idx].Message)
		}
		if !strings.Contains(checks[idx].Message, "no server running") {
			t.Errorf("checks[%d] (%s): expected skip message about no server, got %q",
				idx, checks[idx].Name, checks[idx].Message)
		}
	}
}

func TestRunDoltHealthChecks_ExternalModeNoServer(t *testing.T) {
	// In external/shared server mode, a server IS expected to be running,
	// so connection failure should report real errors.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Write metadata.json marking this as dolt backend
	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Point at a port nothing listens on AND set server mode to external
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59998")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 7 {
		t.Fatalf("expected exactly 7 checks (consistent shape), got %d", len(checks))
	}

	if checks[0].Name != "Dolt Connection" {
		t.Errorf("expected first check to be 'Dolt Connection', got %q", checks[0].Name)
	}
	if checks[0].Status != StatusError {
		t.Errorf("expected StatusError (external server unreachable), got %s: %s", checks[0].Status, checks[0].Message)
	}

	// Schema, Issue Count, Status, and Phantom Databases should be StatusError with skip message
	for _, idx := range []int{1, 2, 3, 5} {
		if checks[idx].Status != StatusError {
			t.Errorf("checks[%d] (%s): expected StatusError, got %s", idx, checks[idx].Name, checks[idx].Status)
		}
		if !strings.Contains(checks[idx].Message, "Skipped (no connection)") {
			t.Errorf("checks[%d] (%s): expected skip message, got %q", idx, checks[idx].Name, checks[idx].Message)
		}
	}
}

func TestRunDoltHealthChecks_CheckNameAndCategory(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) == 0 {
		t.Fatal("expected at least 1 check")
	}

	check := checks[0]
	if check.Category != CategoryCore {
		t.Errorf("expected CategoryCore, got %q", check.Category)
	}
}

// TestLockContention was removed: server-only mode does not acquire advisory
// locks — the server handles its own locking. Lock contention is no longer
// a doctor concern for connection establishment.

func TestServerMode_NoLockAcquired(t *testing.T) {
	// Server-only mode never acquires advisory locks.
	// We force a non-listening port in external mode so the connection always fails.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1") // External mode: server expected

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 7 {
		t.Fatalf("expected exactly 7 checks, got %d", len(checks))
	}

	check := checks[0]

	// Should fail with a connection error, NOT a lock error
	if check.Status != StatusError {
		t.Errorf("expected StatusError (server unreachable), got %s", check.Status)
	}

}

func TestIsWispTable(t *testing.T) {
	tests := []struct {
		name     string
		table    string
		expected bool
	}{
		{"wisps table", "wisps", true},
		{"wisp_events", "wisp_events", true},
		{"wisp_labels", "wisp_labels", true},
		{"wisp_dependencies", "wisp_dependencies", true},
		{"wisp_comments", "wisp_comments", true},
		{"issues table", "issues", false},
		{"events table", "events", false},
		{"labels table", "labels", false},
		{"dependencies table", "dependencies", false},
		{"config table", "config", false},
		{"dolt_ignore", "dolt_ignore", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWispTable(tt.table); got != tt.expected {
				t.Errorf("isWispTable(%q) = %v, want %v", tt.table, got, tt.expected)
			}
		})
	}
}

