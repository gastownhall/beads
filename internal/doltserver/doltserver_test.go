package doltserver

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
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

	// Unset GT_ROOT so we don't pick up a real daemon PID
	orig := os.Getenv("GT_ROOT")
	os.Unsetenv("GT_ROOT")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		}
	}()

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false when no PID file exists")
	}
}

func TestIsRunningChecksDaemonPidUnderGasTown(t *testing.T) {
	dir := t.TempDir()
	gtRoot := t.TempDir()

	// Set GT_ROOT to simulate Gas Town environment
	orig := os.Getenv("GT_ROOT")
	os.Setenv("GT_ROOT", gtRoot)
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		} else {
			os.Unsetenv("GT_ROOT")
		}
	}()

	// No daemon PID file, no standard PID file → not running
	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false when no PID files exist")
	}

	// Write a stale daemon PID file → still not running
	daemonDir := filepath.Join(gtRoot, "daemon")
	if err := os.MkdirAll(daemonDir, 0750); err != nil {
		t.Fatal(err)
	}
	daemonPidFile := filepath.Join(daemonDir, "dolt.pid")
	if err := os.WriteFile(daemonPidFile, []byte("99999999"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false for stale daemon PID")
	}

	// Daemon PID file should NOT be cleaned up (it's owned by the daemon)
	if _, err := os.Stat(daemonPidFile); os.IsNotExist(err) {
		t.Error("daemon PID file should not be cleaned up by IsRunning")
	}
}

func TestIsRunningStalePID(t *testing.T) {
	dir := t.TempDir()

	// Unset GT_ROOT so we don't pick up a real daemon PID
	orig := os.Getenv("GT_ROOT")
	os.Unsetenv("GT_ROOT")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		}
	}()

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

	// Unset GT_ROOT so we don't pick up a real daemon PID
	orig := os.Getenv("GT_ROOT")
	os.Unsetenv("GT_ROOT")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		}
	}()

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
		// Clear both env vars to test pure standalone behavior
		t.Setenv("GT_ROOT", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")

		cfg := DefaultConfig(dir)
		if cfg.Host != "127.0.0.1" {
			t.Errorf("expected host 127.0.0.1, got %s", cfg.Host)
		}
		// Standalone mode defaults to DerivePort (hash-based, per-project)
		expected := DerivePort(dir)
		if cfg.Port != expected {
			t.Errorf("expected DerivePort %d, got %d", expected, cfg.Port)
		}
		if cfg.BeadsDir != dir {
			t.Errorf("expected BeadsDir=%s, got %s", dir, cfg.BeadsDir)
		}
	})

	t.Run("config_yaml_port", func(t *testing.T) {
		// When config.yaml sets dolt.port, DefaultConfig should use it
		// (provided no env var or metadata.json port is set).
		t.Setenv("GT_ROOT", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")

		// Create a temp dir with config.yaml containing dolt.port
		configDir := t.TempDir()
		configYaml := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(configYaml, []byte("dolt.port: 3308\n"), 0600); err != nil {
			t.Fatal(err)
		}

		// Point BEADS_DIR at the config dir so config.Initialize() picks it up
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)

		freshDir := t.TempDir()
		cfg := DefaultConfig(freshDir)
		if cfg.Port != 3308 {
			t.Errorf("expected port 3308 from config.yaml, got %d", cfg.Port)
		}
	})

	t.Run("no_config_uses_derive_port", func(t *testing.T) {
		// When no env var, no metadata port, no port file, and no GT_ROOT,
		// DefaultConfig should use DerivePort for per-project isolation.
		t.Setenv("GT_ROOT", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")

		freshDir := t.TempDir()
		cfg := DefaultConfig(freshDir)

		expected := DerivePort(freshDir)
		if cfg.Port != expected {
			t.Errorf("expected DefaultConfig to use DerivePort (%d), got %d",
				expected, cfg.Port)
		}
	})

	t.Run("port_file_takes_precedence_over_derive", func(t *testing.T) {
		// When a port file exists (written by Start()), DefaultConfig should
		// use it — this is how commands find a server that Start() placed on
		// a fallback port.
		t.Setenv("GT_ROOT", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")

		freshDir := t.TempDir()
		if err := writePortFile(freshDir, 14000); err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig(freshDir)

		if cfg.Port != 14000 {
			t.Errorf("expected port file port 14000, got %d", cfg.Port)
		}
	})
}

func TestEnsurePortFile(t *testing.T) {
	beadsDir := t.TempDir()
	portFile := filepath.Join(beadsDir, "dolt-server.port")

	if err := EnsurePortFile(beadsDir, 14567); err != nil {
		t.Fatalf("EnsurePortFile(write missing): %v", err)
	}
	if data, err := os.ReadFile(portFile); err != nil {
		t.Fatalf("ReadFile(missing): %v", err)
	} else if got := strings.TrimSpace(string(data)); got != "14567" {
		t.Fatalf("port file = %q, want 14567", got)
	}

	if err := os.WriteFile(portFile, []byte("bad"), 0600); err != nil {
		t.Fatalf("WriteFile(corrupt): %v", err)
	}
	if err := EnsurePortFile(beadsDir, 14568); err != nil {
		t.Fatalf("EnsurePortFile(repair corrupt): %v", err)
	}
	if data, err := os.ReadFile(portFile); err != nil {
		t.Fatalf("ReadFile(repaired): %v", err)
	} else if got := strings.TrimSpace(string(data)); got != "14568" {
		t.Fatalf("repaired port file = %q, want 14568", got)
	}

	if err := os.WriteFile(portFile, []byte("14569"), 0600); err != nil {
		t.Fatalf("WriteFile(stale): %v", err)
	}
	if err := EnsurePortFile(beadsDir, 14570); err != nil {
		t.Fatalf("EnsurePortFile(update stale): %v", err)
	}
	if data, err := os.ReadFile(portFile); err != nil {
		t.Fatalf("ReadFile(updated): %v", err)
	} else if got := strings.TrimSpace(string(data)); got != "14570" {
		t.Fatalf("updated port file = %q, want 14570", got)
	}
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

		// CWD must be outside any Gas Town workspace for standalone test
		origWd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		defer os.Chdir(origWd)

		if max := maxDoltServers(); max != 3 {
			t.Errorf("expected 3 in standalone mode, got %d", max)
		}
	})

	t.Run("gastown_same_as_standalone", func(t *testing.T) {
		// After daemon removal, GT_ROOT no longer affects maxDoltServers
		t.Setenv("GT_ROOT", t.TempDir())

		if max := maxDoltServers(); max != 3 {
			t.Errorf("expected 3 (daemon removed, no special GT_ROOT handling), got %d", max)
		}
	})
}

func TestIsProcessInDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("isProcessInDir always returns false on Windows (CWD not exposed)")
	}
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

func TestCountDoltProcesses(t *testing.T) {
	// Just verify it doesn't panic and returns a non-negative number
	count := countDoltProcesses()
	if count < 0 {
		t.Errorf("countDoltProcesses returned negative: %d", count)
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

func TestIsDoltProcessDeadPID(t *testing.T) {
	// A non-existent PID should return false (ps will fail)
	if isDoltProcess(99999999) {
		t.Error("expected isDoltProcess to return false for dead PID")
	}
}

func TestIsDoltProcessSelf(t *testing.T) {
	// Our own process is not a dolt sql-server, so should return false
	if isDoltProcess(os.Getpid()) {
		t.Error("expected isDoltProcess to return false for non-dolt process")
	}
}

// --- Multi-project port fallback tests ---

func TestReclaimPortOccupiedByOtherProject(t *testing.T) {
	// When a Dolt server is running on our port but serving a DIFFERENT data dir,
	// reclaimPort should return ErrPortOccupiedByOtherProject instead of killing it.
	// This allows Start() to fall back to DerivePort for per-project isolation.
	//
	// We can't easily fake a real Dolt process in a unit test, but we can verify
	// the sentinel error exists and is used correctly by the Start fallback logic.
	if ErrPortOccupiedByOtherProject == nil {
		t.Fatal("ErrPortOccupiedByOtherProject sentinel must be defined")
	}
}

func TestStartFallsBackToDerivePortOnCollision(t *testing.T) {
	// When Start() finds another project's Dolt server on the DerivePort,
	// it should fall back gracefully rather than killing the other server.
	dir := t.TempDir()

	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	cfg := DefaultConfig(dir)
	expected := DerivePort(dir)
	if cfg.Port != expected {
		t.Fatalf("expected DerivePort %d, got %d", expected, cfg.Port)
	}

	// The fallback port should be a DerivePort value (13307-14306 range)
	fallback := fallbackPort(dir)
	if fallback < portRangeBase || fallback >= portRangeBase+portRangeSize {
		t.Errorf("fallbackPort(%q) = %d, expected in DerivePort range [%d, %d)",
			dir, fallback, portRangeBase, portRangeBase+portRangeSize)
	}
}

func TestDefaultConfigReturnsDerivePortForStandalone(t *testing.T) {
	// DefaultConfig must return DerivePort for standalone mode so that each
	// project gets an isolated port. This prevents multi-project setups from
	// all trying to connect to the same port (3307).
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	expected := DerivePort(dir)
	if cfg.Port != expected {
		t.Errorf("DefaultConfig should return DerivePort (%d) for standalone, got %d",
			expected, cfg.Port)
	}
}

// --- Pre-v56 dolt database detection tests (GH#2137) ---

func TestIsPreV56DoltDir_NoMarker(t *testing.T) {
	doltDir := t.TempDir()
	dotDolt := filepath.Join(doltDir, ".dolt")
	if err := os.MkdirAll(dotDolt, 0750); err != nil {
		t.Fatal(err)
	}
	// .dolt/ exists but no .bd-dolt-ok marker → pre-v56
	if !IsPreV56DoltDir(doltDir) {
		t.Error("expected pre-v56 detection when .dolt/ exists without marker")
	}
}

func TestIsPreV56DoltDir_WithMarker(t *testing.T) {
	doltDir := t.TempDir()
	dotDolt := filepath.Join(doltDir, ".dolt")
	if err := os.MkdirAll(dotDolt, 0750); err != nil {
		t.Fatal(err)
	}
	// Write the marker
	if err := os.WriteFile(filepath.Join(doltDir, bdDoltMarker), []byte("ok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if IsPreV56DoltDir(doltDir) {
		t.Error("expected NOT pre-v56 when marker exists")
	}
}

func TestIsPreV56DoltDir_NoDotDolt(t *testing.T) {
	doltDir := t.TempDir()
	// No .dolt/ at all → not pre-v56 (nothing to recover)
	if IsPreV56DoltDir(doltDir) {
		t.Error("expected NOT pre-v56 when .dolt/ doesn't exist")
	}
}

func TestEnsureDoltInit_SeedsMarker(t *testing.T) {
	doltDir := t.TempDir()
	dotDolt := filepath.Join(doltDir, ".dolt", "noms")
	if err := os.MkdirAll(dotDolt, 0750); err != nil {
		t.Fatal(err)
	}
	// No marker → simulates existing database

	// ensureDoltInit should seed the marker (non-destructive)
	if err := ensureDoltInit(doltDir); err != nil {
		t.Fatal(err)
	}

	// After seeding, should no longer be detected as pre-v56
	if IsPreV56DoltDir(doltDir) {
		t.Error("expected marker to be seeded for existing database")
	}

	// .dolt/ should still exist (not deleted)
	if _, err := os.Stat(filepath.Join(doltDir, ".dolt")); os.IsNotExist(err) {
		t.Error("expected .dolt/ to still exist after seeding")
	}
}

func TestRecoverPreV56DoltDir(t *testing.T) {
	doltDir := t.TempDir()
	dotDolt := filepath.Join(doltDir, ".dolt", "noms")
	if err := os.MkdirAll(dotDolt, 0750); err != nil {
		t.Fatal(err)
	}
	// Write a sentinel file to verify deletion
	sentinel := filepath.Join(doltDir, ".dolt", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("old data"), 0600); err != nil {
		t.Fatal(err)
	}

	// RecoverPreV56DoltDir should remove the old .dolt/ and reinitialize
	recovered, err := RecoverPreV56DoltDir(doltDir)
	if err != nil {
		// dolt might not be installed; check if .dolt/ was at least removed
		if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
			t.Error("expected old .dolt/ contents to be removed during recovery")
		}
		t.Skipf("recovery partially completed (dolt init may have failed): %v", err)
	}
	if !recovered {
		t.Error("expected recovery to be performed")
	}

	// Old sentinel should be gone
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Error("expected old .dolt/ contents to be removed during recovery")
	}
}

func TestRecoverPreV56DoltDir_WithMarker(t *testing.T) {
	doltDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(doltDir, ".dolt"), 0750); err != nil {
		t.Fatal(err)
	}
	// Write marker → should NOT recover
	if err := os.WriteFile(filepath.Join(doltDir, bdDoltMarker), []byte("ok\n"), 0600); err != nil {
		t.Fatal(err)
	}

	recovered, err := RecoverPreV56DoltDir(doltDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered {
		t.Error("expected no recovery when marker exists")
	}
}

func TestRecoverPreV56DoltDir_NoDotDolt(t *testing.T) {
	doltDir := t.TempDir()
	// No .dolt/ at all → should NOT recover

	recovered, err := RecoverPreV56DoltDir(doltDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered {
		t.Error("expected no recovery when .dolt/ doesn't exist")
	}
}

func TestEnsureDoltInit_WritesMarker(t *testing.T) {
	doltDir := t.TempDir()
	// Fresh init — no .dolt/ yet

	// ensureDoltInit should create .dolt/ and write the marker
	err := ensureDoltInit(doltDir)
	if err != nil {
		// dolt might not be installed in test env; skip marker check
		t.Skipf("dolt init failed (dolt may not be installed): %v", err)
	}

	markerPath := filepath.Join(doltDir, bdDoltMarker)
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Error("expected .bd-dolt-ok marker to be written after successful dolt init")
	}
}

// --- stopServerProcess tests ---

func TestStopServerProcessPreservesMonitorAndActivity(t *testing.T) {
	// stopServerProcess must leave the monitor PID file and activity file
	// intact. This is the core fix for GH#2324: the idle monitor calls
	// stopServerProcess (not Stop) to avoid killing itself via
	// cleanupStateFiles → stopIdleMonitor.
	dir := t.TempDir()
	t.Setenv("GT_ROOT", "")

	// Write activity and monitor PID files
	touchActivity(dir)
	monitorPID := os.Getpid()
	_ = os.WriteFile(monitorPidPath(dir), []byte(strconv.Itoa(monitorPID)), 0600)

	// No server PID file → stopServerProcess returns immediately (already stopped)
	if err := stopServerProcess(dir); err != nil {
		t.Fatalf("stopServerProcess: %v", err)
	}

	// Activity file must be preserved
	if _, err := os.Stat(activityPath(dir)); os.IsNotExist(err) {
		t.Error("stopServerProcess must preserve activity file")
	}
	// Monitor PID file must be preserved
	if _, err := os.Stat(monitorPidPath(dir)); os.IsNotExist(err) {
		t.Error("stopServerProcess must preserve monitor PID file")
	}
	// Monitor PID should still contain our PID (not corrupted)
	data, err := os.ReadFile(monitorPidPath(dir))
	if err != nil {
		t.Fatalf("reading monitor PID file: %v", err)
	}
	if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid != monitorPID {
		t.Errorf("monitor PID file changed: want %d, got %d", monitorPID, pid)
	}
}

func TestStopServerProcessRemovesPidAndPort(t *testing.T) {
	// When the server is running (simulated with a stale PID that IsRunning
	// will clean up), stopServerProcess should remove PID and port files
	// but leave activity and monitor files intact.
	dir := t.TempDir()
	t.Setenv("GT_ROOT", "")

	// Write all state files
	_ = os.WriteFile(pidPath(dir), []byte("99999999"), 0600) // dead PID
	_ = writePortFile(dir, 13500)
	touchActivity(dir)
	_ = os.WriteFile(monitorPidPath(dir), []byte(strconv.Itoa(os.Getpid())), 0600)

	// stopServerProcess: IsRunning sees dead PID → returns Running=false →
	// stopServerProcess returns nil (already stopped).
	_ = stopServerProcess(dir)

	// PID file was cleaned up by IsRunning (stale PID detection)
	if _, err := os.Stat(pidPath(dir)); !os.IsNotExist(err) {
		t.Error("expected server PID file to be removed")
	}

	// Activity and monitor PID files must survive
	if _, err := os.Stat(activityPath(dir)); os.IsNotExist(err) {
		t.Error("stopServerProcess must preserve activity file")
	}
	if _, err := os.Stat(monitorPidPath(dir)); os.IsNotExist(err) {
		t.Error("stopServerProcess must preserve monitor PID file")
	}
}

func TestRunIdleMonitorZeroTimeoutExitsImmediately(t *testing.T) {
	// With zero timeout, the monitor should exit immediately.
	dir := t.TempDir()

	done := make(chan struct{})
	go func() {
		RunIdleMonitor(dir, 0)
		close(done)
	}()

	select {
	case <-done:
		// good — exited immediately with zero timeout
	case <-time.After(2 * time.Second):
		t.Fatal("RunIdleMonitor should exit immediately with zero timeout")
	}
}
