//go:build integration && !windows

package doltserver_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// idleActivityDirName is doltserver's dolt-server.activity directory.
const idleActivityDirName = "dolt-server.activity"

// startIdleWatchedServerForTest auto-starts the workspace's server with the
// idle watcher wired to an in-process goroutine (the CLI spawns a detached
// re-exec; the test binary cannot, so RunIdleWatch runs on a goroutine with
// a context the test cancels). The spawner names this test process as the
// watcher, so watcher.pid matches the goroutine's os.Getpid(). It returns
// the port, the options of every watcher launched, and the watched PID.
func startIdleWatchedServerForTest(t *testing.T, beadsDir string) (port int, spawned *[]doltserver.IdleWatchOpts, pid int) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var launches []doltserver.IdleWatchOpts
	orig := doltserver.IdleWatchSpawner
	doltserver.IdleWatchSpawner = func(opts doltserver.IdleWatchOpts) (int, error) {
		mu.Lock()
		launches = append(launches, opts)
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = doltserver.RunIdleWatch(ctx, opts)
		}()
		return os.Getpid(), nil
	}
	// One cleanup, in the order it needs: cancel first (so the watchers'
	// sleeps return), then wait for the goroutines to finish (a watcher can
	// still be inside its reap when the test body ends, and its stop reads
	// the config the setup cleanup resets), then restore the hook. Cleanups
	// run LIFO, so this runs before setupLifecycleTestDir's
	// config.ResetForTesting.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		doltserver.IdleWatchSpawner = orig
	})

	port, startedByUs, err := doltserver.EnsureRunningDetailed(beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	if !startedByUs {
		t.Fatal("EnsureRunningDetailed on a fresh workspace must start the server")
	}
	mu.Lock()
	n := len(launches)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("the auto-start launched %d idle watchers, want 1", n)
	}
	return port, &launches, pidFromFile(t, beadsDir)
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

// waitForIdleReap polls until the server's pid file is gone and its port is
// closed, or fails after timeout.
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

// TestLifecycle_IdleWatchReapsIdleServer is GH#4282's acceptance on a real
// server: an auto-started server with a short idle window and no client
// activity stops itself through the `bd dolt stop` path (pid and port files
// and watcher.pid removed, the dirty working set flushed to a commit), and
// the next EnsureRunning auto-starts a fresh server with a new watcher.
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

	// The stop went through the stop path: no pid or port file remains, the
	// watcher's record is gone, and the log names the reason and remedy.
	for name, path := range map[string]string{
		"pid file":    filepath.Join(beadsDir, doltserver.PIDFileName),
		"port file":   filepath.Join(beadsDir, doltserver.PortFileName),
		"watcher.pid": filepath.Join(beadsDir, idleActivityDirName, "watcher.pid"),
	} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s still exists after the idle reap", name)
		}
	}
	logData, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.log"))
	if err != nil {
		t.Fatalf("reading dolt-server.log: %v", err)
	}
	if !strings.Contains(string(logData), "with no clients (dolt.idle-timeout); the next bd command restarts it") {
		t.Errorf("server log does not record the idle stop:\n%s", logData)
	}

	// The next open auto-starts a new server and a new watcher for it, and
	// the dirty working set the reap flushed is a real commit.
	port2, startedByUs, err := doltserver.EnsureRunningDetailed(beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed after reap: %v", err)
	}
	if !startedByUs {
		t.Error("EnsureRunningDetailed after the reap found a server running, want a fresh auto-start")
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

// TestLifecycle_IdleWatchPrunesDeadClientLease pins the crash half of the
// lease rule on a real server: a lease whose process is alive keeps the
// idle server up across several windows; once that process dies, the
// watcher prunes its lease and reaps the server within a window plus a poll.
func TestLifecycle_IdleWatchPrunesDeadClientLease(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "4s")
	port, _, pid := startIdleWatchedServerForTest(t, beadsDir)
	if p, err := os.FindProcess(pid); err == nil {
		reg.Register(p)
	}
	running := func() bool {
		data, err := os.ReadFile(filepath.Join(beadsDir, doltserver.PIDFileName))
		return err == nil && strings.TrimSpace(string(data)) == fmt.Sprint(pid)
	}

	// A lease named for another process, the way a bd store in another
	// process looks to the watcher; that process later dies without
	// releasing it.
	helper := exec.Command("sleep", "60")
	if err := helper.Start(); err != nil {
		t.Fatalf("starting lease-owning helper: %v", err)
	}
	defer func() { _ = helper.Process.Kill(); _ = helper.Wait() }()
	leasePath := filepath.Join(beadsDir, idleActivityDirName,
		fmt.Sprintf("client-%d-0a0b0c0d0e0f", helper.Process.Pid))
	if err := os.WriteFile(leasePath, nil, 0o600); err != nil {
		t.Fatalf("writing helper lease: %v", err)
	}

	// The helper is alive, so its lease keeps the server up across windows.
	time.Sleep(9 * time.Second)
	if !running() {
		t.Fatal("server was reaped while a live helper's lease protected it")
	}

	// The helper dies: the watcher prunes the dead lease and reaps.
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("killing lease-owning helper: %v", err)
	}
	_ = helper.Wait()
	waitForIdleReap(t, beadsDir, port, 30*time.Second)
	if _, err := os.Stat(leasePath); err == nil {
		t.Error("the dead helper's lease was not pruned")
	}
	_ = doltserver.IgnoreNotRunning(doltserver.Stop(beadsDir))
}
