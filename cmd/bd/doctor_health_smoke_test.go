package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunCheckHealth_UnreachableServer exercises the DSN-building branch in
// runCheckHealth (bd-h5k7). With metadata.json pointing at an unreachable
// port, the code should resolve Password via GetDoltServerPasswordForPort
// without panicking, fail the ping silently, and return.
func TestRunCheckHealth_UnreachableServer(t *testing.T) {
	// Force the resolved port to 1 (guaranteed unreachable) so we don't
	// depend on any real server.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{
  "database": "beads.db",
  "dolt_mode": "server",
  "dolt_server_host": "127.0.0.1",
  "dolt_server_user": "root",
  "dolt_database": "beads"
}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	// Should not panic. Silent exit on ping failure is the expected path.
	runCheckHealth(tmpDir)
}
