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

	t.Run("no_config_uses_default_port_not_hash", func(t *testing.T) {
		// BUG: configfile.DefaultDoltServerPort is 3307, but DefaultConfig
		// falls through to DerivePort() (range 13307-14306) when no explicit
		// port is configured. This means users with a shared Homebrew Dolt
		// server on 3307 get a wrong hash-derived port.
		//
		// Expected: when no env var, no metadata port, and no GT_ROOT,
		// DefaultConfig should use configfile.DefaultDoltServerPort (3307)
		// as the fallback, NOT DerivePort().

		orig := os.Getenv("GT_ROOT")
		os.Unsetenv("GT_ROOT")
		origPort := os.Getenv("BEADS_DOLT_SERVER_PORT")
		os.Unsetenv("BEADS_DOLT_SERVER_PORT")
		defer func() {
			if orig != "" {
				os.Setenv("GT_ROOT", orig)
			}
			if origPort != "" {
				os.Setenv("BEADS_DOLT_SERVER_PORT", origPort)
			}
		}()

		freshDir := t.TempDir()
		cfg := DefaultConfig(freshDir)

		// The default port should match configfile.DefaultDoltServerPort (3307),
		// not a hash-derived port in the 13307-14306 range.
		if cfg.Port != 3307 {
			t.Errorf("expected DefaultConfig to use configfile.DefaultDoltServerPort (3307), got %d", cfg.Port)
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

// --- Gas Town detection heuristic tests ---

func TestHasGasTownPathSegment(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// Positive cases: Gas Town rig structures
		{"crew worktree", "/home/user/gt/beads/crew/leeloo/.beads", true},
		{"polecats worktree", "/home/user/gt/beads/polecats/worker1/.beads", true},
		{"refinery rig", "/home/user/gt/beads/refinery/rig/.beads", true},
		{"witness dir", "/home/user/gt/beads/witness/session/.beads", true},
		{"deacon dir", "/home/user/gt/beads/deacon/data/.beads", true},
		{"mayor rig", "/home/user/gt/beads/mayor/rig/.beads", true},

		// Negative cases: standalone beads projects
		{"standalone project", "/home/user/projects/myapp/.beads", false},
		{"tmp dir", "/tmp/test/.beads", false},
		{"home beads", "/home/user/.beads", false},

		// Edge cases: substrings should NOT match
		{"screwdriver not crew", "/home/user/screwdriver/project/.beads", false},
		{"polecats-data exact not prefix", "/home/user/polecats-data/.beads", false},
		{"unrefinedery not refinery", "/home/user/unrefinedery/.beads", false},

		// Exact directory component matches only
		{"crew as exact component", "/crew/something", true},
		{"witness deep nested", "/a/b/c/witness/d/e", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasGasTownPathSegment(tt.path)
			if got != tt.want {
				t.Errorf("HasGasTownPathSegment(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsDaemonManagedGTROOT(t *testing.T) {
	// When GT_ROOT is set, IsDaemonManaged should return true regardless of path
	orig := os.Getenv("GT_ROOT")
	os.Setenv("GT_ROOT", "/some/gt/root")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		} else {
			os.Unsetenv("GT_ROOT")
		}
	}()

	if !IsDaemonManaged() {
		t.Error("expected IsDaemonManaged()=true when GT_ROOT is set")
	}
}

func TestIsDaemonManagedNoGTROOTStandalone(t *testing.T) {
	// When GT_ROOT is unset and we're NOT in a Gas Town worktree,
	// IsDaemonManaged should return false.
	orig := os.Getenv("GT_ROOT")
	os.Unsetenv("GT_ROOT")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		}
	}()

	// Save and change to a non-Gas-Town directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if IsDaemonManaged() {
		t.Error("expected IsDaemonManaged()=false in standalone dir without GT_ROOT")
	}
}

func TestIsDaemonManagedNoGTROOTCrewPath(t *testing.T) {
	// When GT_ROOT is unset but we're in a Gas Town crew worktree,
	// IsDaemonManaged should return true via path segment heuristic.
	orig := os.Getenv("GT_ROOT")
	os.Unsetenv("GT_ROOT")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		}
	}()

	crewDir := filepath.Join(t.TempDir(), "gt", "beads", "crew", "testworker")
	if err := os.MkdirAll(crewDir, 0750); err != nil {
		t.Fatal(err)
	}
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(crewDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if !IsDaemonManaged() {
		t.Error("expected IsDaemonManaged()=true in crew dir even without GT_ROOT")
	}
}

func TestIsGasTownRoot(t *testing.T) {
	// Create a fake Gas Town root with marker subdirectories
	gtRoot := t.TempDir()
	for _, marker := range []string{"daemon", "deacon", "warrants", "mayor"} {
		if err := os.MkdirAll(filepath.Join(gtRoot, marker), 0750); err != nil {
			t.Fatal(err)
		}
	}

	if !isGasTownRoot(gtRoot) {
		t.Error("expected isGasTownRoot=true for dir with daemon+deacon+warrants+mayor")
	}

	// A dir with only 1 marker should NOT match
	singleMarker := t.TempDir()
	if err := os.MkdirAll(filepath.Join(singleMarker, "daemon"), 0750); err != nil {
		t.Fatal(err)
	}
	if isGasTownRoot(singleMarker) {
		t.Error("expected isGasTownRoot=false for dir with only one marker")
	}

	// Empty dir should NOT match
	if isGasTownRoot(t.TempDir()) {
		t.Error("expected isGasTownRoot=false for empty dir")
	}
}

func TestWalkUpForGasTownRoot(t *testing.T) {
	// Build: gtRoot/beads/.beads — walk up from .beads should find gtRoot
	gtRoot := t.TempDir()
	for _, marker := range []string{"daemon", "deacon"} {
		if err := os.MkdirAll(filepath.Join(gtRoot, marker), 0750); err != nil {
			t.Fatal(err)
		}
	}
	beadsDir := filepath.Join(gtRoot, "beads", ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	if !walkUpForGasTownRoot(beadsDir) {
		t.Error("expected walkUpForGasTownRoot=true when ancestor is GT root")
	}

	// From a completely unrelated directory, should return false
	if walkUpForGasTownRoot(t.TempDir()) {
		t.Error("expected walkUpForGasTownRoot=false for standalone dir")
	}
}

func TestIsDaemonManagedNoGTROOTGTRootCWD(t *testing.T) {
	// When GT_ROOT is unset but CWD is the Gas Town root itself
	// (no crew/mayor/etc in path), walk-up should detect it.
	orig := os.Getenv("GT_ROOT")
	os.Unsetenv("GT_ROOT")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		}
	}()

	// Create fake GT root with markers
	gtRoot := t.TempDir()
	for _, marker := range []string{"daemon", "deacon"} {
		if err := os.MkdirAll(filepath.Join(gtRoot, marker), 0750); err != nil {
			t.Fatal(err)
		}
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(gtRoot); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if !IsDaemonManaged() {
		t.Error("expected IsDaemonManaged()=true when CWD is GT root (with daemon+deacon)")
	}
}

func TestIsDaemonManagedForBeadsDir(t *testing.T) {
	// When GT_ROOT is unset and CWD is unrelated, but beadsDir
	// is under a Gas Town root, IsDaemonManagedFor should detect it.
	orig := os.Getenv("GT_ROOT")
	os.Unsetenv("GT_ROOT")
	defer func() {
		if orig != "" {
			os.Setenv("GT_ROOT", orig)
		}
	}()

	// Create fake GT root
	gtRoot := t.TempDir()
	for _, marker := range []string{"daemon", "warrants"} {
		if err := os.MkdirAll(filepath.Join(gtRoot, marker), 0750); err != nil {
			t.Fatal(err)
		}
	}
	beadsDir := filepath.Join(gtRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// CWD is a completely unrelated temp dir
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// Without beadsDir — should be false
	if IsDaemonManaged() {
		t.Error("expected IsDaemonManaged()=false when CWD is unrelated")
	}
	// With beadsDir — should be true
	if !IsDaemonManagedFor(beadsDir) {
		t.Error("expected IsDaemonManagedFor()=true when beadsDir is under GT root")
	}
}
