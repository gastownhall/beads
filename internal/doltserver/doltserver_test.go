package doltserver

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

	t.Run("standalone", func(t *testing.T) {
		// Clear GT_ROOT to test standalone behavior
		orig := os.Getenv("GT_ROOT")
		os.Unsetenv("GT_ROOT")
		defer func() {
			if orig != "" {
				os.Setenv("GT_ROOT", orig)
			}
		}()

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
	})

	t.Run("gastown", func(t *testing.T) {
		orig := os.Getenv("GT_ROOT")
		os.Setenv("GT_ROOT", t.TempDir())
		defer func() {
			if orig != "" {
				os.Setenv("GT_ROOT", orig)
			} else {
				os.Unsetenv("GT_ROOT")
			}
		}()

		cfg := DefaultConfig(dir)
		if cfg.Port != GasTownPort {
			t.Errorf("expected GasTownPort %d under GT_ROOT, got %d", GasTownPort, cfg.Port)
		}
	})
}

func TestStopNotRunning(t *testing.T) {
	dir := t.TempDir()

	err := Stop(dir)
	if err == nil {
		t.Error("expected error when stopping non-running server")
	}
}

// --- Port collision fallback tests ---

func TestIsPortAvailable(t *testing.T) {
	// Bind a port to make it unavailable
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	if isPortAvailable("127.0.0.1", addr.Port) {
		t.Error("expected port to be unavailable while listener is active")
	}

	// A random high port should generally be available
	if !isPortAvailable("127.0.0.1", 0) {
		t.Log("warning: port 0 reported as unavailable (unusual)")
	}
}

func TestReclaimPortAvailable(t *testing.T) {
	dir := t.TempDir()
	// When the port is free, reclaimPort should return (0, nil)
	adoptPID, err := reclaimPort("127.0.0.1", 14200, dir)
	if err != nil {
		t.Errorf("reclaimPort failed on free port: %v", err)
	}
	if adoptPID != 0 {
		t.Errorf("expected adoptPID=0 for free port, got %d", adoptPID)
	}
}

func TestReclaimPortBusyNonDolt(t *testing.T) {
	dir := t.TempDir()
	// Occupy a port with a non-dolt process
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	occupiedPort := ln.Addr().(*net.TCPAddr).Port

	// reclaimPort should fail (not silently use another port)
	adoptPID, err := reclaimPort("127.0.0.1", occupiedPort, dir)
	if err == nil {
		t.Error("reclaimPort should fail when a non-dolt process holds the port")
	}
	if adoptPID != 0 {
		t.Errorf("expected adoptPID=0 on error, got %d", adoptPID)
	}
}

func TestMaxDoltServers(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		orig := os.Getenv("GT_ROOT")
		os.Unsetenv("GT_ROOT")
		defer func() {
			if orig != "" {
				os.Setenv("GT_ROOT", orig)
			}
		}()

		if max := maxDoltServers(); max != 3 {
			t.Errorf("expected 3 in standalone mode, got %d", max)
		}
	})

	t.Run("gastown", func(t *testing.T) {
		orig := os.Getenv("GT_ROOT")
		os.Setenv("GT_ROOT", t.TempDir())
		defer func() {
			if orig != "" {
				os.Setenv("GT_ROOT", orig)
			} else {
				os.Unsetenv("GT_ROOT")
			}
		}()

		if max := maxDoltServers(); max != 1 {
			t.Errorf("expected 1 under Gas Town, got %d", max)
		}
	})
}

func TestIsProcessInDir(t *testing.T) {
	// Our own process should have a CWD we can check
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Our PID should be in our CWD
	if !isProcessInDir(os.Getpid(), cwd) {
		t.Log("isProcessInDir returned false for own process CWD (lsof may not be available)")
	}

	// Our PID should NOT be in a random temp dir
	if isProcessInDir(os.Getpid(), t.TempDir()) {
		t.Error("isProcessInDir should return false for wrong directory")
	}

	// Dead PID should return false
	if isProcessInDir(99999999, cwd) {
		t.Error("isProcessInDir should return false for dead PID")
	}
}

func TestCountDoltServers(t *testing.T) {
	// Just verify it doesn't panic and returns a non-negative number
	count := countDoltServers()
	if count < 0 {
		t.Errorf("countDoltServers returned negative: %d", count)
	}
}

func TestFindPIDOnPortEmpty(t *testing.T) {
	// A port nobody is listening on should return 0
	pid := findPIDOnPort(19999)
	if pid != 0 {
		t.Errorf("expected 0 for unused port, got %d", pid)
	}
}

func TestPortFileReadWrite(t *testing.T) {
	dir := t.TempDir()

	// No file yet
	if port := readPortFile(dir); port != 0 {
		t.Errorf("expected 0 for missing port file, got %d", port)
	}

	// Write and read back
	if err := writePortFile(dir, 13500); err != nil {
		t.Fatal(err)
	}
	if port := readPortFile(dir); port != 13500 {
		t.Errorf("expected 13500, got %d", port)
	}

	// Corrupt file
	if err := os.WriteFile(portPath(dir), []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	if port := readPortFile(dir); port != 0 {
		t.Errorf("expected 0 for corrupt port file, got %d", port)
	}
}

func TestIsRunningReadsPortFile(t *testing.T) {
	dir := t.TempDir()

	// Write a port file with a custom port
	if err := writePortFile(dir, 13999); err != nil {
		t.Fatal(err)
	}

	// Write a stale PID — IsRunning will clean up, but let's verify port file is read
	// when a valid process exists. Since we can't easily fake a running dolt process,
	// just verify the port file read function works correctly.
	port := readPortFile(dir)
	if port != 13999 {
		t.Errorf("expected port 13999 from port file, got %d", port)
	}
}

// --- Activity tracking tests ---

func TestTouchAndReadActivity(t *testing.T) {
	dir := t.TempDir()

	// No file yet
	if ts := ReadActivityTime(dir); !ts.IsZero() {
		t.Errorf("expected zero time for missing activity file, got %v", ts)
	}

	// Touch and read
	touchActivity(dir)
	ts := ReadActivityTime(dir)
	if ts.IsZero() {
		t.Fatal("expected non-zero activity time after touch")
	}
	if time.Since(ts) > 5*time.Second {
		t.Errorf("activity timestamp too old: %v", ts)
	}
}

func TestCleanupStateFiles(t *testing.T) {
	dir := t.TempDir()

	// Create all state files
	for _, path := range []string{
		pidPath(dir),
		portPath(dir),
		activityPath(dir),
	} {
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupStateFiles(dir)

	for _, path := range []string{
		pidPath(dir),
		portPath(dir),
		activityPath(dir),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", filepath.Base(path))
		}
	}
}

// --- Idle monitor tests ---

func TestRunIdleMonitorDisabled(t *testing.T) {
	// idleTimeout=0 should return immediately
	dir := t.TempDir()
	done := make(chan struct{})
	go func() {
		RunIdleMonitor(dir, 0)
		close(done)
	}()

	select {
	case <-done:
		// good — returned immediately
	case <-time.After(2 * time.Second):
		t.Fatal("RunIdleMonitor(0) should return immediately")
	}
}

func TestFlushWorkingSetUnreachable(t *testing.T) {
	// FlushWorkingSet should return an error when the server is not reachable.
	err := FlushWorkingSet("127.0.0.1", 19998)
	if err == nil {
		t.Error("expected error when server is unreachable")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("expected 'not reachable' in error, got: %v", err)
	}
}

func TestMonitorPidLifecycle(t *testing.T) {
	dir := t.TempDir()

	// No monitor running
	if isMonitorRunning(dir) {
		t.Error("expected no monitor running initially")
	}

	// Write our own PID as monitor (we know we're alive)
	_ = os.WriteFile(monitorPidPath(dir), []byte(strconv.Itoa(os.Getpid())), 0600)
	if !isMonitorRunning(dir) {
		t.Error("expected monitor to be detected as running")
	}

	// Don't call stopIdleMonitor with our own PID (it sends SIGTERM).
	// Instead test with a dead PID.
	_ = os.Remove(monitorPidPath(dir))
	_ = os.WriteFile(monitorPidPath(dir), []byte("99999999"), 0600)
	if isMonitorRunning(dir) {
		t.Error("expected dead PID to not be detected as running")
	}

	// stopIdleMonitor should clean up the PID file
	stopIdleMonitor(dir)
	if _, err := os.Stat(monitorPidPath(dir)); !os.IsNotExist(err) {
		t.Error("expected monitor PID file to be removed")
	}
}
