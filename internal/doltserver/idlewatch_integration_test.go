//go:build integration && !windows

package doltserver_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// startIdleWatchedServerForTest auto-starts the workspace's server with the
// idle watcher wired to an in-process goroutine (the CLI spawns a detached
// re-exec; the test binary cannot, so RunIdleWatch runs on a goroutine with
// a context the test cancels). It returns the port, the spawner-recorded
// opts slice, and the watched PID read from the pid file.
func startIdleWatchedServerForTest(t *testing.T, beadsDir string) (port int, spawned *[]doltserver.IdleWatchOpts, pid int) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var launches []doltserver.IdleWatchOpts
	doltserver.SetIdleWatchSpawner(func(opts doltserver.IdleWatchOpts) {
		mu.Lock()
		launches = append(launches, opts)
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			doltserver.RunIdleWatch(ctx, opts)
		}()
	})
	// One cleanup, in the order it needs: cancel first (so the watchers'
	// sleeps return), then wait for the goroutines to actually finish (a
	// watcher can still be inside its reap when the test body ends, and its
	// stopLocked reads the config the setup cleanup resets), then remove
	// the hook. Cleanups run LIFO, so this runs before setupLifecycleTestDir's
	// config.ResetForTesting.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		doltserver.SetIdleWatchSpawner(nil)
	})

	port, startedByUs, err := doltserver.EnsureRunningDetailed(beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	if !startedByUs {
		t.Fatal("EnsureRunningDetailed on a fresh workspace must start the server")
	}
	pidData, err := os.ReadFile(filepath.Join(beadsDir, doltserver.PIDFileName))
	if err != nil {
		t.Fatalf("reading pid file: %v", err)
	}
	var pidVal int
	if _, err := fmt.Sscan(strings.TrimSpace(string(pidData)), &pidVal); err != nil {
		t.Fatalf("parsing pid file %q: %v", pidData, err)
	}
	return port, &launches, pidVal
}

// waitForIdleReap polls until the server's state files are gone and its port
// is closed, or fails after timeout.
func waitForIdleReap(t *testing.T, beadsDir string, port int, timeout time.Duration) {
	t.Helper()
	pidFile := filepath.Join(beadsDir, doltserver.PIDFileName)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidFile); os.IsNotExist(err) {
			waitForPortClosed(t, port, 5*time.Second)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("server still running with its pid file after %v of idle watch", timeout)
}

// TestLifecycle_IdleWatchReapsIdleServer is GH#4282's acceptance: an
// auto-started server with a short idle window and no client activity stops
// itself through the `bd dolt stop` path — pid/port/activity state files
// removed, the dirty working set flushed to a commit, and the next
// EnsureRunning auto-starts a fresh server.
func TestLifecycle_IdleWatchReapsIdleServer(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "6s")
	port, spawned, pid := startIdleWatchedServerForTest(t, beadsDir)
	if p, err := os.FindProcess(pid); err == nil {
		reg.Register(p)
	}

	// Leave a dirty working set behind, then disconnect: nothing holds the
	// server open and no client touches it for the window.
	db := connectMySQL(t, port)
	if _, err := db.Exec("CREATE TABLE idle_reap_probe (id INT PRIMARY KEY, val VARCHAR(100))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO idle_reap_probe VALUES (1, 'dirty')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	_ = db.Close()

	// 6s window, 1s polls, plus a graceful stop: well inside 30s.
	waitForIdleReap(t, beadsDir, port, 30*time.Second)

	// The stop went through cleanupStateFiles: no pid, port or activity
	// state remains, and the log names the reason and the remedy.
	for name, path := range map[string]string{
		"pid file":     filepath.Join(beadsDir, doltserver.PIDFileName),
		"port file":    filepath.Join(beadsDir, doltserver.PortFileName),
		"activity dir": filepath.Join(beadsDir, doltserver.ActivityDirName),
	} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s still exists after the idle reap", name)
		}
	}
	logData, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.log"))
	if err != nil {
		t.Fatalf("reading dolt-server.log: %v", err)
	}
	if !strings.Contains(string(logData), "bd idle-watch: server stopped after") {
		t.Errorf("server log does not record the idle stop:\n%s", logData)
	}

	// The next open auto-starts a new server and a new watcher for it, and
	// the dirty working set the reap flushed is a real commit.
	port2, _, err := doltserver.EnsureRunningDetailed(beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed after reap: %v", err)
	}
	if p, err := os.FindProcess(pidFromFile(t, beadsDir)); err == nil {
		reg.Register(p)
	}
	if len(*spawned) != 2 {
		t.Errorf("spawned %d watchers, want 2 (one per auto-start)", len(*spawned))
	}
	db2 := connectMySQL(t, port2)
	defer db2.Close()
	var flushes int
	if err := db2.QueryRow("SELECT COUNT(*) FROM dolt_log WHERE message = 'auto-flush: commit working set before server stop'").Scan(&flushes); err != nil {
		t.Fatalf("querying dolt_log: %v", err)
	}
	if flushes < 1 {
		t.Error("the idle reap did not flush the dirty working set to a commit")
	}
	var val string
	if err := db2.QueryRow("SELECT val FROM idle_reap_probe WHERE id = 1").Scan(&val); err != nil {
		t.Fatalf("the dirty working set's table did not survive the reap: %v", err)
	}
	if val != "dirty" {
		t.Errorf("probe row = %q, want %q", val, "dirty")
	}
	_ = doltserver.IgnoreNotRunning(doltserver.Stop(beadsDir))
}

// pidFromFile reads the PID the pid file currently names.
func pidFromFile(t *testing.T, beadsDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, doltserver.PIDFileName))
	if err != nil {
		t.Fatalf("reading pid file: %v", err)
	}
	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(data)), &pid); err != nil {
		t.Fatalf("parsing pid file %q: %v", data, err)
	}
	return pid
}

// TestLifecycle_IdleWatchKeepsServerWithShortLivedClients pins the two
// client shapes that must never be reaped (GH#4282): connections that open
// and close between the watcher's polls (visible only as a Connections
// delta), and one connection held open across several windows (visible only
// as Threads_connected). Only the quiet that follows is reaped.
func TestLifecycle_IdleWatchKeepsServerWithShortLivedClients(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "5s")
	port, _, pid := startIdleWatchedServerForTest(t, beadsDir)
	if p, err := os.FindProcess(pid); err == nil {
		reg.Register(p)
	}
	running := func() bool {
		data, err := os.ReadFile(filepath.Join(beadsDir, doltserver.PIDFileName))
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(data)) == fmt.Sprint(pid)
	}

	// Short-lived clients: an open and close every 200ms for 8s — several
	// per 1s poll, each gone before the next poll reads the counters.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		db := connectMySQL(t, port)
		if err := db.Ping(); err != nil {
			t.Fatalf("churn client: %v", err)
		}
		_ = db.Close()
		time.Sleep(200 * time.Millisecond)
	}
	if !running() {
		t.Fatal("server was reaped while short-lived clients kept connecting")
	}

	// One held connection across more than two 5s windows: no new
	// connections, only Threads_connected above the watcher's own dial.
	held := connectMySQL(t, port)
	defer held.Close()
	if err := held.Ping(); err != nil {
		t.Fatalf("held client: %v", err)
	}
	time.Sleep(11 * time.Second)
	if !running() {
		t.Fatal("server was reaped while a client held its connection open")
	}

	// Quiet at last: the same window that spared the clients reaps the
	// server once nothing touches it.
	_ = held.Close()
	waitForIdleReap(t, beadsDir, port, 30*time.Second)
	_ = doltserver.IgnoreNotRunning(doltserver.Stop(beadsDir))
}
