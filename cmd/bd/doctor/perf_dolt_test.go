//go:build cgo

package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoltPerformanceDiagnostics_RequiresServer(t *testing.T) {
	// Server-only mode: diagnostics require a running dolt sql-server.
	// Without a server, RunDoltPerformanceDiagnostics should return an error.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setupDoltTestDir(t, beadsDir)

	_, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err == nil {
		t.Fatal("expected error when no dolt server is running")
	}
	if !strings.Contains(err.Error(), "not running") && !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("expected server-not-running error, got: %v", err)
	}
}
