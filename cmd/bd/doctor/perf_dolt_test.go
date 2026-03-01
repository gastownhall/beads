//go:build cgo

package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestRunDoltPerformanceDiagnostics_RequiresServer(t *testing.T) {
	// Server-only mode: diagnostics require a running dolt sql-server.
	// Without a server, RunDoltPerformanceDiagnostics should return an error.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write config pointing to a nonexistent database
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = doctorTestServerPort()
	cfg.DoltDatabase = "doctest_perf_nonexistent"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	_, err := RunDoltPerformanceDiagnostics(tmpDir, false)
	if err == nil {
		t.Fatal("expected error when no dolt server is running")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "not running") && !strings.Contains(errStr, "not reachable") && !strings.Contains(errStr, "database not found") {
		t.Errorf("expected server/database error, got: %v", err)
	}
}
