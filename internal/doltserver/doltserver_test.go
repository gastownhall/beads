package doltserver

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

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

func TestKillStaleServersPreservesOtherRepoServers(t *testing.T) {
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
		{"0", true},
		{"false", true},
		{"FALSE", true},
		{"False", true},
		{"1", false},
		{"true", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run("env="+tt.envVal, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_AUTO_START", tt.envVal)
			if got := isAutoStartDisabled(); got != tt.want {
				t.Errorf("isAutoStartDisabled() = %v, want %v", got, tt.want)
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
