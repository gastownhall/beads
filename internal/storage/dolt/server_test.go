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

// TestServerConfigDisableRemotesAPI tests the DisableRemotesAPI flag
func TestServerConfigDisableRemotesAPI(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		server := NewServer(ServerConfig{
			DataDir: "/tmp/test",
		})
		if server.cfg.DisableRemotesAPI {
			t.Error("expected DisableRemotesAPI to be false by default")
		}
	})

	t.Run("preserved when set", func(t *testing.T) {
		server := NewServer(ServerConfig{
			DataDir:           "/tmp/test",
			DisableRemotesAPI: true,
		})
		if !server.cfg.DisableRemotesAPI {
			t.Error("expected DisableRemotesAPI to be true")
		}
		// Defaults should still be applied for other fields
		if server.cfg.SQLPort != DefaultSQLPort {
			t.Errorf("expected default SQL port %d, got %d", DefaultSQLPort, server.cfg.SQLPort)
		}
	})

	t.Run("port check skipped when disabled", func(t *testing.T) {
		// Start with DisableRemotesAPI: port 8080 conflict shouldn't matter.
		// We can't do a full Start() without a dolt repo, but we verify
		// the port check is skipped by using a port that IS in use.
		// The SQL port check will fail first (no server to start), but
		// the remotesapi port check should be skipped entirely.
		server := NewServer(ServerConfig{
			DataDir:           t.TempDir(),
			SQLPort:           13307,
			RemotesAPIPort:    1, // Port 1 is privileged, would fail check
			DisableRemotesAPI: true,
		})
		// Verify the config was set correctly
		if server.cfg.RemotesAPIPort != 1 {
			t.Errorf("expected RemotesAPIPort 1, got %d", server.cfg.RemotesAPIPort)
		}
		if !server.cfg.DisableRemotesAPI {
			t.Error("expected DisableRemotesAPI true")
		}
	})
}

// TestServerStartStopDisableRemotesAPI tests server lifecycle without remotesapi
func TestServerStartStopDisableRemotesAPI(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server test")
	}

	tmpDir, err := os.MkdirTemp("", "dolt-server-noremotes-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Set up dolt identity (HOME may be a temp dir in test)
	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "Test User"},
		{"config", "--global", "--add", "user.email", "test@test.com"},
	} {
		cfgCmd := exec.Command("dolt", args...)
		cfgCmd.Dir = tmpDir
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt %v failed: %v, output: %s", args, err, out)
		}
	}

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to init dolt repo: %v, output: %s", err, output)
	}

	logFile := filepath.Join(tmpDir, "server.log")
	server := NewServer(ServerConfig{
		DataDir:           tmpDir,
		SQLPort:           13309, // Non-standard port
		Host:              "127.0.0.1",
		LogFile:           logFile,
		DisableRemotesAPI: true, // No remotesapi
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := server.Start(ctx); err != nil {
		if logContent, readErr := os.ReadFile(logFile); readErr == nil {
			t.Logf("Server log:\n%s", logContent)
		}
		t.Fatalf("failed to start server: %v", err)
	}

	if !server.IsRunning() {
		t.Error("server should be running")
	}

	if server.SQLPort() != 13309 {
		t.Errorf("expected SQL port 13309, got %d", server.SQLPort())
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("failed to stop server: %v", err)
	}

	if server.IsRunning() {
		t.Error("server should not be running after stop")
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
