//go:build integration && !windows

package doltserver_test

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// setupLifecycleTestDir creates a temp .beads directory with an initialized
// dolt database. Returns the beadsDir path.
func setupLifecycleTestDir(t *testing.T) string {
	t.Helper()
	integration.RequireDolt(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0700); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	cmd := exec.Command("dolt", "init")
	cmd.Dir = doltDir
	cmd.Env = append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %v\n%s", err, out)
	}

	// Ensure no shared server mode interference.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")

	return beadsDir
}

// connectMySQL opens a MySQL connection to the dolt server.
// Caller is responsible for closing the returned *sql.DB.
func connectMySQL(t *testing.T, port int) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	return db
}

// TestLifecycle_StartStopCycle verifies the basic server lifecycle:
// Start → verify state files → connect → execute SQL → Stop → verify cleanup.
// Would have caught: GH#2542 (zombie servers not cleaning up state files).
func TestLifecycle_StartStopCycle(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Start server.
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !state.Running {
		t.Fatal("Start returned state.Running=false")
	}
	if state.PID == 0 {
		t.Fatal("Start returned PID=0")
	}
	if state.Port == 0 {
		t.Fatal("Start returned Port=0")
	}
	// Track for cleanup.
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	t.Logf("Server started: PID=%d Port=%d", state.PID, state.Port)

	// Verify state files exist.
	pidFile := filepath.Join(beadsDir, doltserver.PIDFileName)
	portFile := filepath.Join(beadsDir, doltserver.PortFileName)
	if !integration.FileExists(pidFile) {
		t.Error("PID file does not exist after Start")
	}
	if !integration.FileExists(portFile) {
		t.Error("port file does not exist after Start")
	}

	// Connect and execute SQL.
	db := connectMySQL(t, state.Port)
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS lifecycle_test (id INT PRIMARY KEY, val VARCHAR(100))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO lifecycle_test VALUES (1, 'hello')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var val string
	if err := db.QueryRow("SELECT val FROM lifecycle_test WHERE id = 1").Scan(&val); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if val != "hello" {
		t.Fatalf("expected 'hello', got %q", val)
	}
	_ = db.Close()

	// Stop server.
	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reg.Deregister(state.PID)

	// Verify process is dead.
	time.Sleep(500 * time.Millisecond)
	if integration.IsProcessAlive(state.PID) {
		t.Errorf("process %d is still alive after Stop", state.PID)
	}

	// Verify state files removed.
	if integration.FileExists(pidFile) {
		t.Error("PID file still exists after Stop")
	}
	if integration.FileExists(portFile) {
		t.Error("port file still exists after Stop")
	}
}

// TestLifecycle_CrashRecovery verifies that after a forced kill (SIGKILL),
// a new Start() cleans up stale state and the data survives.
// Would have caught: GH#2636 (infinite restart loop with zombie processes).
func TestLifecycle_CrashRecovery(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Start server and insert data.
	state1, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start (first): %v", err)
	}
	if p, err := os.FindProcess(state1.PID); err == nil {
		reg.Register(p)
	}

	db := connectMySQL(t, state1.Port)
	if _, err := db.Exec("CREATE TABLE crash_test (id INT PRIMARY KEY, val VARCHAR(100))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO crash_test VALUES (1, 'survive')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// Commit the data so it persists.
	if _, err := db.Exec("CALL DOLT_COMMIT('-Am', 'crash test data')"); err != nil {
		t.Logf("DOLT_COMMIT: %v (may be expected if auto-commit is on)", err)
	}
	_ = db.Close()

	// Force-kill the server (simulate crash).
	proc, err := os.FindProcess(state1.PID)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	_, _ = proc.Wait()
	reg.Deregister(state1.PID)

	t.Logf("Server PID %d killed, stale state files left behind", state1.PID)

	// Verify stale PID file still exists (crash didn't clean up).
	pidFile := filepath.Join(beadsDir, doltserver.PIDFileName)
	if !integration.FileExists(pidFile) {
		t.Log("PID file was already cleaned up (unexpected but not fatal)")
	}

	// Start server again — should clean up stale state and work.
	state2, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start (second): %v", err)
	}
	if !state2.Running {
		t.Fatal("second Start returned Running=false")
	}
	if state2.PID == state1.PID {
		t.Error("second Start reused the same PID (unexpected)")
	}
	if p, err := os.FindProcess(state2.PID); err == nil {
		reg.Register(p)
	}

	t.Logf("Server restarted: PID=%d Port=%d", state2.PID, state2.Port)

	// Verify data survived the crash.
	db2 := connectMySQL(t, state2.Port)
	var val string
	err = db2.QueryRow("SELECT val FROM crash_test WHERE id = 1").Scan(&val)
	if err != nil {
		t.Fatalf("SELECT after crash recovery: %v", err)
	}
	if val != "survive" {
		t.Fatalf("expected 'survive', got %q", val)
	}
	_ = db2.Close()

	// Clean stop.
	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop (cleanup): %v", err)
	}
	reg.Deregister(state2.PID)
}

// TestLifecycle_RestartDataPersistence verifies data persists across clean
// Stop → Start cycles.
// Would have caught: GH#2756 (cold-start regression losing working set).
func TestLifecycle_RestartDataPersistence(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Start, write data, stop.
	state1, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start (first): %v", err)
	}
	if p, err := os.FindProcess(state1.PID); err == nil {
		reg.Register(p)
	}

	db := connectMySQL(t, state1.Port)
	if _, err := db.Exec("CREATE TABLE persist_test (id INT PRIMARY KEY, val VARCHAR(100))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO persist_test VALUES (42, 'persistent')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := db.Exec("CALL DOLT_COMMIT('-Am', 'persistence test')"); err != nil {
		t.Logf("DOLT_COMMIT: %v (may be expected)", err)
	}
	_ = db.Close()

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reg.Deregister(state1.PID)

	// Restart and verify data.
	state2, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start (second): %v", err)
	}
	if p, err := os.FindProcess(state2.PID); err == nil {
		reg.Register(p)
	}

	db2 := connectMySQL(t, state2.Port)
	var val string
	if err := db2.QueryRow("SELECT val FROM persist_test WHERE id = 42").Scan(&val); err != nil {
		t.Fatalf("SELECT after restart: %v", err)
	}
	if val != "persistent" {
		t.Fatalf("expected 'persistent', got %q", val)
	}
	_ = db2.Close()

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop (cleanup): %v", err)
	}
	reg.Deregister(state2.PID)
}

// TestLifecycle_MultiRepoIsolation verifies that KillStaleServers for repo A
// does not affect repo B's server.
// Would have caught: GH#2595 (stale cleanup kills healthy servers from other repos).
func TestLifecycle_MultiRepoIsolation(t *testing.T) {
	beadsDirA := setupLifecycleTestDir(t)
	beadsDirB := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diagA := integration.NewDiagnostics(t, beadsDirA)
	diagA.CaptureOnFailure()
	diagB := integration.NewDiagnostics(t, beadsDirB)
	diagB.CaptureOnFailure()

	// Start both servers.
	stateA, err := doltserver.Start(beadsDirA)
	if err != nil {
		t.Fatalf("Start(A): %v", err)
	}
	if p, err := os.FindProcess(stateA.PID); err == nil {
		reg.Register(p)
	}
	t.Logf("Repo A: PID=%d Port=%d", stateA.PID, stateA.Port)

	stateB, err := doltserver.Start(beadsDirB)
	if err != nil {
		t.Fatalf("Start(B): %v", err)
	}
	if p, err := os.FindProcess(stateB.PID); err == nil {
		reg.Register(p)
	}
	t.Logf("Repo B: PID=%d Port=%d", stateB.PID, stateB.Port)

	// KillStaleServers on repo A.
	killed, err := doltserver.KillStaleServers(beadsDirA)
	if err != nil {
		t.Fatalf("KillStaleServers(A): %v", err)
	}
	t.Logf("KillStaleServers(A) killed %d processes: %v", len(killed), killed)

	// Verify B's server is still alive.
	time.Sleep(500 * time.Millisecond)
	if !integration.IsProcessAlive(stateB.PID) {
		t.Errorf("repo B's server (PID %d) was killed by KillStaleServers(A)", stateB.PID)
	}

	// Verify B is still connectable.
	dbB := connectMySQL(t, stateB.Port)
	if _, err := dbB.Exec("SELECT 1"); err != nil {
		t.Errorf("repo B's server not connectable after KillStaleServers(A): %v", err)
	}
	_ = dbB.Close()

	// Cleanup both.
	_ = doltserver.Stop(beadsDirA)
	reg.Deregister(stateA.PID)
	_ = doltserver.Stop(beadsDirB)
	reg.Deregister(stateB.PID)
}

// TestLifecycle_ConcurrentIsRunningStart verifies that concurrent IsRunning()
// calls during a Start() don't cause data races or incorrect state.
// Would have caught: TOCTOU bugs in PID file reads during server startup.
func TestLifecycle_ConcurrentIsRunningStart(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	var wg sync.WaitGroup
	var startState atomic.Pointer[doltserver.State]
	var startErr atomic.Pointer[error]
	ready := make(chan struct{})

	// Goroutine 1: Start the server.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ready
		state, err := doltserver.Start(beadsDir)
		if err != nil {
			startErr.Store(&err)
			return
		}
		startState.Store(state)
	}()

	// Goroutines 2-6: Concurrent IsRunning checks.
	const readers = 5
	isRunningErrors := make(chan error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			for j := 0; j < 20; j++ {
				state, err := doltserver.IsRunning(beadsDir)
				if err != nil {
					isRunningErrors <- err
					return
				}
				// state.Running can be true or false — either is valid during startup.
				_ = state
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	// Release all goroutines.
	close(ready)
	wg.Wait()
	close(isRunningErrors)

	// Check for Start errors.
	if ep := startErr.Load(); ep != nil {
		t.Fatalf("Start failed: %v", *ep)
	}

	state := startState.Load()
	if state == nil {
		t.Fatal("Start returned nil state")
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	// Check for IsRunning errors (panics, data races).
	for err := range isRunningErrors {
		t.Errorf("IsRunning error during concurrent access: %v", err)
	}

	// Cleanup.
	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestLifecycle_PIDReuseDetection verifies that IsRunning returns false when
// the PID file points to a non-dolt process (PID was reused by the OS).
// Would have caught: False-positive IsRunning when PID recycled.
func TestLifecycle_PIDReuseDetection(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Start a non-dolt process (sleep) and write its PID to the PID file.
	sleepCmd := exec.Command("sleep", "60")
	if err := sleepCmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = sleepCmd.Process.Signal(syscall.SIGTERM)
		_ = sleepCmd.Wait()
	})

	sleepPID := sleepCmd.Process.Pid
	t.Logf("sleep process PID: %d", sleepPID)

	// Write the sleep PID as if it were the dolt server.
	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.WriteStalePID(sleepPID)
	corruptor.WriteStalePort(3306) // Arbitrary port.

	// IsRunning should detect this is NOT a dolt process and return false.
	state, err := doltserver.IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if state.Running {
		t.Error("IsRunning returned true for a non-dolt PID — isDoltProcess check failed")
	}

	// Verify stale state files were cleaned up.
	if integration.FileExists(corruptor.PIDFilePath()) {
		t.Error("PID file not cleaned up after detecting non-dolt PID")
	}
}
