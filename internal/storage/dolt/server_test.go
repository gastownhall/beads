//go:build cgo

package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestServerStartStop tests basic server lifecycle
func TestServerStartStop(t *testing.T) {
	// Skip if dolt is not available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server test")
	}

	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "dolt-server-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to init dolt repo: %v, output: %s", err, output)
	}
	t.Logf("dolt init output: %s", output)

	// Use non-standard ports to avoid conflicts
	logFile := filepath.Join(tmpDir, "server.log")
	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13306, // Non-standard port
		RemotesAPIPort: 18080, // Non-standard port
		Host:           "127.0.0.1",
		LogFile:        logFile,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start server
	if err := server.Start(ctx); err != nil {
		// Read log file for debugging
		if logContent, readErr := os.ReadFile(logFile); readErr == nil {
			t.Logf("Server log:\n%s", logContent)
		}
		t.Fatalf("failed to start server: %v", err)
	}

	// Verify server is running
	if !server.IsRunning() {
		t.Error("server should be running")
	}

	// Verify ports
	if server.SQLPort() != 13306 {
		t.Errorf("expected SQL port 13306, got %d", server.SQLPort())
	}
	if server.RemotesAPIPort() != 18080 {
		t.Errorf("expected remotesapi port 18080, got %d", server.RemotesAPIPort())
	}

	// Verify DSN format
	dsn := server.DSN("testdb")
	expected := "root@tcp(127.0.0.1:13306)/testdb"
	if dsn != expected {
		t.Errorf("expected DSN %q, got %q", expected, dsn)
	}

	// Stop server
	if err := server.Stop(); err != nil {
		t.Fatalf("failed to stop server: %v", err)
	}

	// Verify server is not running
	if server.IsRunning() {
		t.Error("server should not be running after stop")
	}
}

// TestServerConfigDefaults tests that config defaults are applied correctly
func TestServerConfigDefaults(t *testing.T) {
	server := NewServer(ServerConfig{
		DataDir: "/tmp/test",
	})

	if server.cfg.SQLPort != DefaultSQLPort {
		t.Errorf("expected default SQL port %d, got %d", DefaultSQLPort, server.cfg.SQLPort)
	}
	if server.cfg.RemotesAPIPort != DefaultRemotesAPIPort {
		t.Errorf("expected default remotesapi port %d, got %d", DefaultRemotesAPIPort, server.cfg.RemotesAPIPort)
	}
	if server.cfg.Host != "127.0.0.1" {
		t.Errorf("expected default host 127.0.0.1, got %s", server.cfg.Host)
	}
	if server.cfg.User != "root" {
		t.Errorf("expected default user root, got %s", server.cfg.User)
	}
}

// TestGetRunningServerPID tests the PID file detection
func TestGetRunningServerPID(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "dolt-pid-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// No PID file should return 0
	if pid := GetRunningServerPID(tmpDir); pid != 0 {
		t.Errorf("expected 0 for non-existent PID file, got %d", pid)
	}

	// Create fake PID file with non-existent PID
	pidFile := filepath.Join(tmpDir, "dolt-server.pid")
	if err := os.WriteFile(pidFile, []byte("999999"), 0600); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	// Should return 0 for non-running process
	if pid := GetRunningServerPID(tmpDir); pid != 0 {
		t.Errorf("expected 0 for non-running process, got %d", pid)
	}
}
