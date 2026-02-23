package doltserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDerivePort(t *testing.T) {
	// Deterministic: same path gives same port
	port1 := DerivePort("/home/user/project/.beads")
	port2 := DerivePort("/home/user/project/.beads")
	if port1 != port2 {
		t.Errorf("same path gave different ports: %d vs %d", port1, port2)
	}

	// Different paths give different ports (with high probability)
	port3 := DerivePort("/home/user/other-project/.beads")
	if port1 == port3 {
		t.Logf("warning: different paths gave same port (possible but unlikely): %d", port1)
	}
}

func TestDerivePortRange(t *testing.T) {
	// Test many paths to verify range
	paths := []string{
		"/a", "/b", "/c", "/tmp/foo", "/home/user/project",
		"/var/data/repo", "/opt/work/beads", "/Users/test/.beads",
		"/very/long/path/to/a/project/directory/.beads",
		"/another/unique/path",
	}

	for _, p := range paths {
		port := DerivePort(p)
		if port < portRangeBase || port >= portRangeBase+portRangeSize {
			t.Errorf("DerivePort(%q) = %d, outside range [%d, %d)",
				p, port, portRangeBase, portRangeBase+portRangeSize)
		}
	}
}

func TestIsRunningNoServer(t *testing.T) {
	dir := t.TempDir()

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false when no PID file exists")
	}
}

func TestIsRunningStalePID(t *testing.T) {
	dir := t.TempDir()

	// Write a PID file with a definitely-dead PID
	pidFile := filepath.Join(dir, "dolt-server.pid")
	// PID 99999999 almost certainly doesn't exist
	if err := os.WriteFile(pidFile, []byte("99999999"), 0600); err != nil {
		t.Fatal(err)
	}

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false for stale PID")
	}

	// PID file should have been cleaned up
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}
}

func TestIsRunningCorruptPID(t *testing.T) {
	dir := t.TempDir()

	pidFile := filepath.Join(dir, "dolt-server.pid")
	if err := os.WriteFile(pidFile, []byte("not-a-number"), 0600); err != nil {
		t.Fatal(err)
	}

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false for corrupt PID file")
	}

	// PID file should have been cleaned up
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected corrupt PID file to be removed")
	}
}

func TestDefaultConfig(t *testing.T) {
	dir := t.TempDir()

	cfg := DefaultConfig(dir)
	if cfg.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1, got %s", cfg.Host)
	}
	if cfg.Port < portRangeBase || cfg.Port >= portRangeBase+portRangeSize {
		t.Errorf("expected port in range [%d, %d), got %d",
			portRangeBase, portRangeBase+portRangeSize, cfg.Port)
	}
	if cfg.BeadsDir != dir {
		t.Errorf("expected BeadsDir=%s, got %s", dir, cfg.BeadsDir)
	}
}

func TestStopNotRunning(t *testing.T) {
	dir := t.TempDir()

	err := Stop(dir)
	if err == nil {
		t.Error("expected error when stopping non-running server")
	}
}
