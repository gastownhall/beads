package doltserver

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

func TestAllocateEphemeralPort(t *testing.T) {
	// Should return a valid port in the ephemeral range
	port, err := allocateEphemeralPort("127.0.0.1")
	if err != nil {
		t.Fatalf("allocateEphemeralPort: %v", err)
	}
	if port < 1024 || 65535 < port {
		t.Errorf("port %d outside valid range [1024, 65535]", port)
	}

	// Multiple calls should return different ports (with very high probability)
	port2, err := allocateEphemeralPort("127.0.0.1")
	if err != nil {
		t.Fatalf("allocateEphemeralPort (2nd call): %v", err)
	}
	if port == port2 {
		t.Logf("warning: two consecutive allocations returned the same port %d (unlikely)", port)
	}

	// The returned port should be available for binding
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port2)))
	if err != nil {
		t.Logf("warning: allocated port %d not immediately bindable (TOCTOU): %v", port2, err)
	} else {
		_ = ln.Close()
	}
}

func TestAllocateEphemeralPortIPv6(t *testing.T) {
	// Should work with IPv6 loopback if available
	port, err := allocateEphemeralPort("::1")
	if err != nil {
		t.Skipf("IPv6 loopback not available: %v", err)
	}
	if port < 1024 || 65535 < port {
		t.Errorf("port %d outside valid range [1024, 65535]", port)
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

func TestIsRunningChecksDaemonPidUnderOrchestrator(t *testing.T) {
	dir := t.TempDir()
	gtRoot := t.TempDir()

	// Set GT_ROOT to simulate orchestrator environment
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

func TestIsRunningStalePIDRemovesPortFile(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(pidPath(dir), []byte("99999999"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(dir, 14567); err != nil {
		t.Fatal(err)
	}

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false for stale PID")
	}
	if _, err := os.Stat(portPath(dir)); !os.IsNotExist(err) {
		t.Error("expected stale port file to be removed")
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

func TestIsRunningCorruptPIDRemovesPortFile(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(pidPath(dir), []byte("not-a-number"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(dir, 14567); err != nil {
		t.Fatal(err)
	}

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false for corrupt PID file")
	}
	if _, err := os.Stat(portPath(dir)); !os.IsNotExist(err) {
		t.Error("expected stale port file to be removed")
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Run("standalone_returns_zero_port", func(t *testing.T) {
		// Clear env vars to test pure standalone behavior
		t.Setenv("GT_ROOT", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")

		freshDir := t.TempDir()
		cfg := DefaultConfig(freshDir)
		if cfg.Host != "127.0.0.1" {
			t.Errorf("expected host 127.0.0.1, got %s", cfg.Host)
		}
		// No configured port source → port 0 (ephemeral allocation on Start)
		if cfg.Port != 0 {
			t.Errorf("expected port 0 (ephemeral) when no port source configured, got %d", cfg.Port)
		}
		if cfg.BeadsDir != freshDir {
			t.Errorf("expected BeadsDir=%s, got %s", freshDir, cfg.BeadsDir)
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

	t.Run("no_config_returns_zero_port", func(t *testing.T) {
		// When no env var, no metadata port, no port file, and no config.yaml,
		// DefaultConfig should return port 0 (ephemeral allocation on Start).
		t.Setenv("GT_ROOT", "")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")

		freshDir := t.TempDir()
		cfg := DefaultConfig(freshDir)

		if cfg.Port != 0 {
			t.Errorf("expected port 0 (ephemeral) when no port source, got %d", cfg.Port)
		}
	})

	t.Run("port_file_takes_precedence_over_ephemeral", func(t *testing.T) {
		// When a port file exists (written by Start()), DefaultConfig should
		// use it.
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

		// CWD must be outside any orchestrator workspace for standalone test
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

	t.Run("orchestrator_same_as_standalone", func(t *testing.T) {
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

// --- IsRunning port-zero orphan recovery ---

func TestIsRunningOrphanNoPortFile(t *testing.T) {
	// When PID file exists but process is dead and no port file,
	// IsRunning should clean up and return Running=false.
	// (The orphan kill path for *live* processes requires a real dolt server,
	// but the dead-PID cleanup path exercises the same code structure.)
	dir := t.TempDir()
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	// Write PID file with dead PID, no port file
	if err := os.WriteFile(pidPath(dir), []byte("99999999"), 0600); err != nil {
		t.Fatal(err)
	}

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Error("expected Running=false for dead PID with no port file")
	}
	// PID file should be cleaned up
	if _, err := os.Stat(pidPath(dir)); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
}

func TestCleanupStateFiles(t *testing.T) {
	dir := t.TempDir()

	// Create all state files
	for _, path := range []string{
		pidPath(dir),
		portPath(dir),
	} {
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupStateFiles(dir)

	for _, path := range []string{
		pidPath(dir),
		portPath(dir),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", filepath.Base(path))
		}
	}
}

// TestStopNotRunningCleansUpStateFiles verifies that calling Stop when the server
// is not running still removes leftover PID/port files, so bd dolt status won't
// report stale state (GH#2670).
func TestStopNotRunningCleansUpStateFiles(t *testing.T) {
	dir := t.TempDir()

	// Create stale PID/port files pointing to a non-existent process
	if err := os.WriteFile(pidPath(dir), []byte("999999999"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portPath(dir), []byte("13307"), 0600); err != nil {
		t.Fatal(err)
	}

	// Stop should return ErrServerNotRunning but still clean up files
	err := Stop(dir)
	if !errors.Is(err, ErrServerNotRunning) {
		t.Fatalf("expected ErrServerNotRunning, got: %v", err)
	}

	// Verify state files were cleaned up
	if _, statErr := os.Stat(pidPath(dir)); !os.IsNotExist(statErr) {
		t.Error("PID file should be removed after Stop on dead server")
	}
	if _, statErr := os.Stat(portPath(dir)); !os.IsNotExist(statErr) {
		t.Error("port file should be removed after Stop on dead server")
	}
}

// TestCleanupStateFilesReturnsError verifies that cleanupStateFiles returns
// errors when removal fails for reasons other than NotExist (e.g., permission denied).
func TestCleanupStateFilesReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not effective on Windows")
	}
	dir := t.TempDir()

	// Create a PID file then make the directory read-only so removal fails.
	if err := os.WriteFile(pidPath(dir), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0755) })

	err := cleanupStateFiles(dir)
	if err == nil {
		t.Error("expected error when directory is read-only, got nil")
	}
}

// TestCleanupStateFilesNoFiles verifies cleanupStateFiles returns nil
// when no state files exist (already clean).
func TestCleanupStateFilesNoFiles(t *testing.T) {
	dir := t.TempDir()
	err := cleanupStateFiles(dir)
	if err != nil {
		t.Errorf("expected nil for missing files, got: %v", err)
	}
}

// TestStopNoStateFiles verifies Stop on an empty directory (no PID/port files)
// returns ErrServerNotRunning with no cleanup errors.
func TestStopNoStateFiles(t *testing.T) {
	dir := t.TempDir()
	err := Stop(dir)
	if !errors.Is(err, ErrServerNotRunning) {
		t.Fatalf("expected ErrServerNotRunning, got: %v", err)
	}
	// Should be the pure sentinel since there are no files to fail on.
	remaining := IgnoreNotRunning(err)
	if remaining != nil {
		t.Errorf("expected no cleanup errors, got: %v", remaining)
	}
}

// TestStopNotRunningWithCleanupError verifies that Stop returns both the
// sentinel and cleanup errors when the server is not running but state
// files can't be removed.
func TestStopNotRunningWithCleanupError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not effective on Windows")
	}
	dir := t.TempDir()

	// Create stale PID file, then make dir read-only.
	if err := os.WriteFile(pidPath(dir), []byte("999999999"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0755) })

	err := Stop(dir)
	if !errors.Is(err, ErrServerNotRunning) {
		t.Fatalf("expected ErrServerNotRunning in error, got: %v", err)
	}
	// Should also contain the cleanup error.
	remaining := IgnoreNotRunning(err)
	if remaining == nil {
		t.Error("expected cleanup error to be preserved, got nil")
	}
}

func TestKillStaleServersPreservesOtherRepoServers(t *testing.T) {
	t.Setenv("BEADS_DOLT_AUTO_START", "") // ensure auto-start guard doesn't short-circuit
	dir := t.TempDir()
	canonicalPID := 111
	sameRepoOrphanPID := 222
	otherRepoPID := 333

	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(canonicalPID)), 0600); err != nil {
		t.Fatal(err)
	}

	var killed []int
	got, err := killStaleServersForDir(
		dir,
		[]int{canonicalPID, sameRepoOrphanPID, otherRepoPID},
		func(pid int, doltDir string) bool {
			if doltDir != ResolveDoltDir(dir) {
				t.Fatalf("unexpected dolt dir: got %q want %q", doltDir, ResolveDoltDir(dir))
			}
			return pid == canonicalPID || pid == sameRepoOrphanPID
		},
		func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("killStaleServersForDir error: %v", err)
	}
	if len(got) != 1 || got[0] != sameRepoOrphanPID {
		t.Fatalf("killed=%v, want [%d]", got, sameRepoOrphanPID)
	}
	if len(killed) != 1 || killed[0] != sameRepoOrphanPID {
		t.Fatalf("kill callback got %v, want [%d]", killed, sameRepoOrphanPID)
	}
}

func TestKillStaleServersWithoutCanonicalPIDIsNoop(t *testing.T) {
	// Without a PID file, beads has no record of starting a server.
	// killStaleServersForDir should be a no-op to avoid killing
	// externally-managed servers (systemd, other repos, etc).
	dir := t.TempDir()
	sameRepoOrphanPID := 222
	otherRepoPID := 333

	var killed []int
	got, err := killStaleServersForDir(
		dir,
		[]int{sameRepoOrphanPID, otherRepoPID},
		func(pid int, _ string) bool {
			return pid == sameRepoOrphanPID
		},
		func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("killStaleServersForDir error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("killed=%v, want [] (no PID file means nothing is stale)", got)
	}
	if len(killed) != 0 {
		t.Fatalf("kill callback got %v, want [] (no PID file means nothing is stale)", killed)
	}
}

func TestKillStaleServersSkipsExplicitPort(t *testing.T) {
	// When metadata.json has an explicit port, the server is externally
	// managed and killStaleServersForDir should be a complete no-op.
	dir := t.TempDir()

	// Write a metadata.json with an explicit port
	metadataPath := filepath.Join(dir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"dolt_server_port": 3307}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Write a PID file (would normally trigger stale cleanup)
	canonicalPID := 111
	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(canonicalPID)), 0600); err != nil {
		t.Fatal(err)
	}

	orphanPID := 222
	var killed []int
	got, err := killStaleServersForDir(
		dir,
		[]int{canonicalPID, orphanPID},
		func(pid int, _ string) bool { return true },
		func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("killStaleServersForDir error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("killed=%v, want [] (explicit port = externally managed)", got)
	}
}

func TestKillStaleServersSkipsAutoStartDisabled(t *testing.T) {
	// When BEADS_DOLT_AUTO_START=0, the server is externally managed
	// and killStaleServersForDir should be a complete no-op.
	dir := t.TempDir()
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	// Write a PID file
	canonicalPID := 111
	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(canonicalPID)), 0600); err != nil {
		t.Fatal(err)
	}

	orphanPID := 222
	var killed []int
	got, err := killStaleServersForDir(
		dir,
		[]int{canonicalPID, orphanPID},
		func(pid int, _ string) bool { return true },
		func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("killStaleServersForDir error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("killed=%v, want [] (auto-start disabled = externally managed)", got)
	}
}

func TestIsAutoStartDisabled(t *testing.T) {
	tests := []struct {
		envVal string
		want   bool
	}{
		// strconv.ParseBool falsy values → disabled
		{"0", true},
		{"f", true},
		{"F", true},
		{"false", true},
		{"FALSE", true},
		{"False", true},
		// backward-compat "off" (case-insensitive) → disabled
		{"off", true},
		{"OFF", true},
		{"Off", true},
		// whitespace-trimmed falsy values → disabled
		{" 0 ", true},
		{" false ", true},
		{"\toff\n", true},
		// whitespace-trimmed truthy value → enabled (not disabled)
		{" true ", false},
		// strconv.ParseBool truthy values → enabled (not disabled)
		{"1", false},
		{"t", false},
		{"T", false},
		{"true", false},
		{"TRUE", false},
		{"True", false},
		// empty / unset → enabled (not disabled)
		{"", false},
		// unrecognized values → enabled (fail-open, not disabled)
		{"no", false},
		{"disabled", false},
		{"nope", false},
	}
	for _, tt := range tests {
		t.Run("env="+tt.envVal, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_AUTO_START", tt.envVal)
			if got := IsAutoStartDisabled(); got != tt.want {
				t.Errorf("IsAutoStartDisabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsAutoStartDisabled_Sources verifies that disable is OR-ed across
// env and config: either source can independently disable auto-start, and
// there is no way to force-enable via one source when the other says disabled.
func TestIsAutoStartDisabled_Sources(t *testing.T) {
	// Initialize config so config.Set/GetString works.
	t.Chdir(t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	tests := []struct {
		name string
		env  string
		cfg  string
		want bool
	}{
		{"env_disabled_config_enabled", "0", "true", true},  // env wins
		{"env_empty_config_disabled", "", "false", true},    // config kicks in
		{"env_empty_config_off", "", "off", true},           // config "off" works
		{"env_empty_config_OFF", "", "OFF", true},           // config case-insensitive
		{"env_empty_config_0", "", "0", true},               // config "0"
		{"env_enabled_config_disabled", "1", "false", true}, // config still disables; env can't force-enable
		{"both_empty", "", "", false},                       // neither set
		{"env_off_config_true", "off", "true", true},        // env wins
		// config with ParseBool-expanded values
		{"env_empty_config_f", "", "f", true},  // config "f"
		{"env_empty_config_F", "", "F", true},  // config "F"
		{"env_t_config_empty", "t", "", false}, // env truthy, config unset
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_AUTO_START", tt.env)
			config.Set("dolt.auto-start", tt.cfg)
			defer config.Set("dolt.auto-start", "")
			if got := IsAutoStartDisabled(); got != tt.want {
				t.Errorf("IsAutoStartDisabled() = %v, want %v (env=%q, cfg=%q)",
					got, tt.want, tt.env, tt.cfg)
			}
		})
	}
}

func TestIgnoreNotRunning(t *testing.T) {
	cleanupErr := errors.New("permission denied")

	tests := []struct {
		name    string
		err     error
		wantNil bool
		wantMsg string
	}{
		{"nil", nil, true, ""},
		{"pure_sentinel", ErrServerNotRunning, true, ""},
		{"joined_sentinel_nil", errors.Join(ErrServerNotRunning, nil), true, ""},
		{"joined_sentinel_cleanup", errors.Join(ErrServerNotRunning, cleanupErr), false, "permission denied"},
		{"unrelated_error", errors.New("connection refused"), false, "connection refused"},
		{"single_wrapped_sentinel", fmt.Errorf("stop: %w", ErrServerNotRunning), true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IgnoreNotRunning(tt.err)
			if tt.wantNil {
				if got != nil {
					t.Errorf("IgnoreNotRunning() = %v, want nil", got)
				}
			} else {
				if got == nil {
					t.Fatal("IgnoreNotRunning() = nil, want error")
				}
				if !strings.Contains(got.Error(), tt.wantMsg) {
					t.Errorf("IgnoreNotRunning() = %q, want containing %q", got, tt.wantMsg)
				}
			}
		})
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

// --- Ephemeral port tests ---

func TestDefaultConfigReturnsZeroForStandalone(t *testing.T) {
	// DefaultConfig must return port 0 for standalone mode (no configured
	// port source). Start() will allocate an ephemeral port from the OS,
	// giving each project a unique port without hash collisions (GH#2098).
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	if cfg.Port != 0 {
		t.Errorf("DefaultConfig should return port 0 (ephemeral) for standalone, got %d",
			cfg.Port)
	}
}

func TestDefaultConfigEnvVarOverridesEphemeral(t *testing.T) {
	// Explicit env var should always take precedence over ephemeral.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "15000")
	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	if cfg.Port != 15000 {
		t.Errorf("expected env var port 15000, got %d", cfg.Port)
	}
}

func TestDefaultConfigPortFileTakesPrecedence(t *testing.T) {
	// Port file (written by Start) should take precedence over ephemeral.
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	dir := t.TempDir()
	if err := writePortFile(dir, 14567); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig(dir)
	if cfg.Port != 14567 {
		t.Errorf("expected port file port 14567, got %d", cfg.Port)
	}
}

func TestReadPortFile_Empty(t *testing.T) {
	// ReadPortFile on a directory with no port file should return 0.
	dir := t.TempDir()
	if p := ReadPortFile(dir); p != 0 {
		t.Errorf("expected 0 for missing port file, got %d", p)
	}
}

func TestReadPortFile_Valid(t *testing.T) {
	dir := t.TempDir()
	if err := writePortFile(dir, 12345); err != nil {
		t.Fatal(err)
	}
	if p := ReadPortFile(dir); p != 12345 {
		t.Errorf("expected 12345, got %d", p)
	}
}

// TestReadPortFile_IgnoresConfigYaml verifies that ReadPortFile only reads
// the port file, NOT config.yaml. This is the crux of the GH#2336 fix:
// bd init uses ReadPortFile instead of DefaultConfig to avoid inheriting
// another project's port from config.yaml or global config.
func TestReadPortFile_IgnoresConfigYaml(t *testing.T) {
	dir := t.TempDir()

	// Write a config.yaml with a dolt port (simulating another project's config)
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("dolt:\n  port: 9999\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// ReadPortFile must return 0 — it should ONLY read the port file,
	// not config.yaml. This prevents cross-project leakage during init.
	if p := ReadPortFile(dir); p != 0 {
		t.Errorf("ReadPortFile should ignore config.yaml, got port %d", p)
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

// --- Shared Server Mode Tests (GH#2377) ---

func TestIsSharedServerMode_Default(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	config.ResetForTesting()
	if IsSharedServerMode() {
		t.Error("expected per-project mode by default")
	}
}

func TestIsSharedServerMode_EnvVar1(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	if !IsSharedServerMode() {
		t.Error("expected shared server mode with BEADS_DOLT_SHARED_SERVER=1")
	}
}

func TestIsSharedServerMode_EnvVarTrue(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "true")
	if !IsSharedServerMode() {
		t.Error("expected shared server mode with BEADS_DOLT_SHARED_SERVER=true")
	}
}

func TestIsSharedServerMode_EnvVarTRUE(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "TRUE")
	if !IsSharedServerMode() {
		t.Error("expected shared server mode with BEADS_DOLT_SHARED_SERVER=TRUE (case-insensitive)")
	}
}

func TestIsSharedServerMode_EnvVar0(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	config.ResetForTesting()
	if IsSharedServerMode() {
		t.Error("expected per-project mode with BEADS_DOLT_SHARED_SERVER=0")
	}
}

func TestSharedServerDir(t *testing.T) {
	dir, err := SharedServerDir()
	if err != nil {
		t.Fatalf("SharedServerDir: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".beads", "shared-server")
	if dir != expected {
		t.Errorf("SharedServerDir = %q, want %q", dir, expected)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Errorf("expected directory to exist: %s", dir)
	}
}

func TestSharedDoltDir(t *testing.T) {
	dir, err := SharedDoltDir()
	if err != nil {
		t.Fatalf("SharedDoltDir: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".beads", "shared-server", "dolt")
	if dir != expected {
		t.Errorf("SharedDoltDir = %q, want %q", dir, expected)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Errorf("expected directory to exist: %s", dir)
	}
}

func TestSharedServerDir_EnvOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", tmp)
	dir, err := SharedServerDir()
	if err != nil {
		t.Fatalf("SharedServerDir: %v", err)
	}
	if dir != tmp {
		t.Errorf("SharedServerDir = %q, want %q (from BEADS_SHARED_SERVER_DIR)", dir, tmp)
	}
}

func TestSharedDoltDir_EnvOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", tmp)
	dir, err := SharedDoltDir()
	if err != nil {
		t.Fatalf("SharedDoltDir: %v", err)
	}
	expected := filepath.Join(tmp, "dolt")
	if dir != expected {
		t.Errorf("SharedDoltDir = %q, want %q", dir, expected)
	}
}

func TestResolveServerDir_PerProject(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	config.ResetForTesting()
	result := resolveServerDir("/some/project/.beads")
	if result != "/some/project/.beads" {
		t.Errorf("expected per-project dir, got %s", result)
	}
}

func TestResolveServerDir_SharedMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	result := resolveServerDir("/some/project/.beads")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".beads", "shared-server")
	if result != expected {
		t.Errorf("resolveServerDir with shared mode = %q, want %q", result, expected)
	}
}

func TestDefaultConfig_SharedModeFixedPort(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	if cfg.Port != DefaultSharedServerPort {
		t.Errorf("shared mode: expected port %d (DefaultSharedServerPort), got %d", DefaultSharedServerPort, cfg.Port)
	}
}

func TestDefaultConfig_SharedModeGeneralPortOverrides(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "5000")
	t.Setenv("GT_ROOT", "")
	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	if cfg.Port != 5000 {
		t.Errorf("BEADS_DOLT_SERVER_PORT should override shared mode default, got %d", cfg.Port)
	}
}

func TestDefaultSharedServerPort_DiffersFromDefault(t *testing.T) {
	if DefaultSharedServerPort == configfile.DefaultDoltServerPort {
		t.Errorf("DefaultSharedServerPort (%d) must differ from DefaultDoltServerPort (%d) to avoid orchestrator conflict",
			DefaultSharedServerPort, configfile.DefaultDoltServerPort)
	}
}

func TestDefaultConfig_SharedModeBeadsDir(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	cfg := DefaultConfig("/some/project/.beads")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".beads", "shared-server")
	if cfg.BeadsDir != expected {
		t.Errorf("DefaultConfig.BeadsDir = %q, want %q", cfg.BeadsDir, expected)
	}
}

// --- ServerMode tests ---

func TestResolveServerMode_Default(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	config.ResetForTesting()

	dir := t.TempDir()
	mode := ResolveServerMode(dir)
	if mode != ServerModeOwned {
		t.Errorf("expected ServerModeOwned for empty dir, got %v", mode)
	}
}

func TestResolveServerMode_SharedServer(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")

	dir := t.TempDir()
	mode := ResolveServerMode(dir)
	if mode != ServerModeExternal {
		t.Errorf("expected ServerModeExternal with shared server, got %v", mode)
	}
}

func TestResolveServerMode_ExplicitPort(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	config.ResetForTesting()

	dir := t.TempDir()
	// Write metadata.json with explicit port
	metaCfg := &configfile.Config{
		DoltServerPort: 3307,
	}
	if err := metaCfg.Save(dir); err != nil {
		t.Fatal(err)
	}

	mode := ResolveServerMode(dir)
	if mode != ServerModeExternal {
		t.Errorf("expected ServerModeExternal with explicit port, got %v", mode)
	}
}

func TestResolveServerMode_ServerModeEnv(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
	config.ResetForTesting()

	dir := t.TempDir()
	mode := ResolveServerMode(dir)
	if mode != ServerModeExternal {
		t.Errorf("expected ServerModeExternal with BEADS_DOLT_SERVER_MODE=1, got %v", mode)
	}
}

func TestResolveServerMode_EmbeddedMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	config.ResetForTesting()

	dir := t.TempDir()
	// Write metadata.json with embedded mode
	metaCfg := &configfile.Config{
		DoltMode: "embedded",
	}
	if err := metaCfg.Save(dir); err != nil {
		t.Fatal(err)
	}

	mode := ResolveServerMode(dir)
	if mode != ServerModeEmbedded {
		t.Errorf("expected ServerModeEmbedded with dolt_mode=embedded, got %v", mode)
	}
}

func TestServerMode_String(t *testing.T) {
	tests := []struct {
		mode ServerMode
		want string
	}{
		{ServerModeOwned, "owned"},
		{ServerModeExternal, "external"},
		{ServerModeEmbedded, "embedded"},
		{ServerMode(99), "ServerMode(99)"},
	}
	for _, tc := range tests {
		if got := tc.mode.String(); got != tc.want {
			t.Errorf("ServerMode(%d).String() = %q, want %q", int(tc.mode), got, tc.want)
		}
	}
}

func TestDefaultConfig_IncludesMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	config.ResetForTesting()

	dir := t.TempDir()
	cfg := DefaultConfig(dir)
	if cfg.Mode != ServerModeOwned {
		t.Errorf("expected DefaultConfig.Mode = Owned for empty dir, got %v", cfg.Mode)
	}
}

// --- Upgrade regression tests (GH#2949) ---
// Verify that runtime env vars override stale metadata.json dolt_mode=embedded
// so that upgrades don't silently switch repos into embedded mode.

func TestResolveServerMode_SharedServerOverridesStaleEmbedded(t *testing.T) {
	// Simulate the GH#2949 bug: metadata.json has dolt_mode=embedded but
	// the user has BEADS_DOLT_SHARED_SERVER enabled. Before the fix,
	// ResolveServerMode returned ServerModeEmbedded; after, ServerModeExternal.
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  "dolt",
		DoltMode: configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	config.ResetForTesting()

	got := ResolveServerMode(beadsDir)
	if got != ServerModeExternal {
		t.Errorf("ResolveServerMode with shared-server + stale embedded = %v, want ServerModeExternal", got)
	}
}

func TestResolveServerMode_ServerModeEnvOverridesStaleEmbedded(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  "dolt",
		DoltMode: configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	config.ResetForTesting()

	got := ResolveServerMode(beadsDir)
	if got != ServerModeExternal {
		t.Errorf("ResolveServerMode with SERVER_MODE=1 + stale embedded = %v, want ServerModeExternal", got)
	}
}

func TestResolveServerMode_EmbeddedHonoredWithoutServerEnv(t *testing.T) {
	// When no server env vars are set, metadata.json embedded mode is correct.
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  "dolt",
		DoltMode: configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	config.ResetForTesting()

	got := ResolveServerMode(beadsDir)
	if got != ServerModeEmbedded {
		t.Errorf("ResolveServerMode with no server env = %v, want ServerModeEmbedded", got)
	}
}

func TestReadyTimeout(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		setEnv   bool
		want     time.Duration
	}{
		{"unset defaults to 10s", "", false, 10 * time.Second},
		{"empty string defaults to 10s", "", true, 10 * time.Second},
		{"valid integer seconds", "120", true, 120 * time.Second},
		{"whitespace is trimmed", "  60  ", true, 60 * time.Second},
		{"invalid string falls back", "notanumber", true, 10 * time.Second},
		{"zero falls back", "0", true, 10 * time.Second},
		{"negative falls back", "-5", true, 10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("BEADS_DOLT_READY_TIMEOUT", tt.envValue)
			} else {
				// Save and restore any pre-existing value so we don't
				// leak env state into subsequent tests.
				if prev, ok := os.LookupEnv("BEADS_DOLT_READY_TIMEOUT"); ok {
					t.Cleanup(func() { os.Setenv("BEADS_DOLT_READY_TIMEOUT", prev) })
				}
				os.Unsetenv("BEADS_DOLT_READY_TIMEOUT")
			}
			got := readyTimeout()
			if got != tt.want {
				t.Errorf("readyTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBuildDoltServerArgs verifies the argv constructed for `dolt sql-server`
// always includes a non-info --loglevel and the expected host/port. This
// pins the fix for the field report where dolt-server.log ballooned to
// hundreds of MB with `msg=NewConnection` / `msg=ConnectionClosed` spam
// because dolt logs connection open/close at INFO by default.
//
// If this test fails because someone intentionally lowered verbosity back
// to info/debug/trace, please instead pick a different mitigation (e.g.
// dolt YAML config) and update doltServerLogLevel plus this test together.
func TestBuildDoltServerArgs(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		port     int
		wantHost string
		wantPort string
	}{
		{
			name:     "loopback ipv4 with ephemeral-style port",
			host:     "127.0.0.1",
			port:     54321,
			wantHost: "127.0.0.1",
			wantPort: "54321",
		},
		{
			name:     "localhost hostname with default dolt port",
			host:     "localhost",
			port:     3306,
			wantHost: "localhost",
			wantPort: "3306",
		},
		{
			name:     "ipv6 loopback with low port",
			host:     "::1",
			port:     1024,
			wantHost: "::1",
			wantPort: "1024",
		},
	}

	// Levels that MUST NOT appear — anything at or below info re-introduces
	// the NewConnection/ConnectionClosed noise we are trying to suppress.
	forbiddenLevels := []string{"trace", "debug", "info"}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := buildDoltServerArgs(tc.host, tc.port)

			if len(args) == 0 || args[0] != "sql-server" {
				t.Fatalf("args[0] = %q, want %q; full args: %v",
					firstOrEmpty(args), "sql-server", args)
			}

			// -H <host>
			hostIdx := indexOf(args, "-H")
			if hostIdx < 0 || hostIdx+1 >= len(args) {
				t.Fatalf("missing -H <host> in args: %v", args)
			}
			if got := args[hostIdx+1]; got != tc.wantHost {
				t.Errorf("host = %q, want %q", got, tc.wantHost)
			}

			// -P <port>
			portIdx := indexOf(args, "-P")
			if portIdx < 0 || portIdx+1 >= len(args) {
				t.Fatalf("missing -P <port> in args: %v", args)
			}
			if got := args[portIdx+1]; got != tc.wantPort {
				t.Errorf("port = %q, want %q", got, tc.wantPort)
			}

			// --loglevel=<level> — the actual fix.
			logLevel, ok := findLogLevel(args)
			if !ok {
				t.Fatalf("missing --loglevel flag in args; got: %v", args)
			}
			for _, bad := range forbiddenLevels {
				if logLevel == bad {
					t.Errorf("--loglevel=%s is too verbose; "+
						"dolt logs NewConnection/ConnectionClosed at INFO, "+
						"which caused the ~380MB dolt-server.log field report. "+
						"Use warning/error/fatal instead.", logLevel)
				}
			}
			// Sanity-check we're using a level dolt actually accepts.
			validLevels := map[string]bool{
				"trace": true, "debug": true, "info": true,
				"warning": true, "error": true, "fatal": true,
			}
			if !validLevels[logLevel] {
				t.Errorf("--loglevel=%s is not a valid dolt log level "+
					"(valid: trace, debug, info, warning, error, fatal)",
					logLevel)
			}
		})
	}
}

func TestWaitForReady(t *testing.T) {
	// Allocate an ephemeral port, then release it so we can re-bind later.
	tmpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not allocate ephemeral port: %v", err)
	}
	addr := tmpListener.Addr().(*net.TCPAddr)
	host := "127.0.0.1"
	port := addr.Port
	if err := tmpListener.Close(); err != nil {
		t.Fatalf("could not release ephemeral port: %v", err)
	}

	// Spawn a goroutine that delays binding the port. This simulates a
	// "slow server" -- the TCP listener is not yet bound when waitForReady
	// is first called.
	bindAfter := 200 * time.Millisecond
	listenerReady := make(chan net.Listener, 1)
	go func() {
		time.Sleep(bindAfter)
		ln, listenErr := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if listenErr != nil {
			close(listenerReady)
			return
		}
		listenerReady <- ln
	}()
	t.Cleanup(func() {
		ln, ok := <-listenerReady
		if ok && ln != nil {
			_ = ln.Close()
		}
	})

	// NOTE: subtests must run in declaration order (the default for
	// non-parallel subtests). "times out" runs before the goroutine binds
	// the port; "succeeds" runs after.
	t.Run("times out when server not ready in time", func(t *testing.T) {
		// 50ms is well under the 200ms bind delay, so this MUST time out.
		if err := waitForReady(host, port, 50*time.Millisecond); err == nil {
			t.Errorf("expected timeout error, got nil")
		}
	})

	t.Run("succeeds when server becomes ready in time", func(t *testing.T) {
		// 2 seconds is well over the remaining bind delay; gives comfortable margin.
		if err := waitForReady(host, port, 2*time.Second); err != nil {
			t.Errorf("expected nil error, got: %v", err)
		}
	})
}

// TestDoltServerLogLevelConstant pins doltServerLogLevel to a non-chatty
// value. It complements TestBuildDoltServerArgs by guarding the constant
// directly, so a refactor that stops calling buildDoltServerArgs cannot
// silently regress the fix.
func TestDoltServerLogLevelConstant(t *testing.T) {
	switch doltServerLogLevel {
	case "warning", "error", "fatal":
		// ok — these all suppress INFO-level NewConnection noise.
	default:
		t.Errorf("doltServerLogLevel = %q; must be one of "+
			"warning/error/fatal to suppress NewConnection/ConnectionClosed "+
			"log spam (see dolt-connection-log-verbosity field report)",
			doltServerLogLevel)
	}
}

// indexOf returns the index of the first occurrence of needle in haystack,
// or -1 if not found. Local helper to keep the test self-contained.
func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}

// findLogLevel extracts the value of a --loglevel=<v> or --loglevel <v>
// flag from argv. Returns the value and true if found.
func findLogLevel(args []string) (string, bool) {
	const prefix = "--loglevel="
	for i, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix), true
		}
		if a == "--loglevel" && i+1 < len(args) {
			return args[i+1], true
		}
		// Short form -l <level>
		if a == "-l" && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func TestGlobalDatabaseConstants(t *testing.T) {
	if GlobalDatabaseName == "" {
		t.Error("GlobalDatabaseName must not be empty")
	}
	if GlobalDatabaseName != "beads_global" {
		t.Errorf("GlobalDatabaseName = %q, want %q", GlobalDatabaseName, "beads_global")
	}
	if GlobalIssuePrefix == "" {
		t.Error("GlobalIssuePrefix must not be empty")
	}
	if GlobalIssuePrefix != "global" {
		t.Errorf("GlobalIssuePrefix = %q, want %q", GlobalIssuePrefix, "global")
	}
	if GlobalProjectID == "" {
		t.Error("GlobalProjectID must not be empty")
	}
	if GlobalProjectID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("GlobalProjectID = %q, want sentinel UUID", GlobalProjectID)
	}
}

func TestEnsureGlobalDatabase_ServerNotReachable(t *testing.T) {
	// EnsureGlobalDatabase should return an error when the server is not reachable.
	err := EnsureGlobalDatabase("127.0.0.1", 19999, "root", "")
	if err == nil {
		t.Error("expected error when server is not reachable")
	}
}
