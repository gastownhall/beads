//go:build integration && !windows

package doltserver_test

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// setupLifecycleTestDir creates a temp .beads directory with an initialized
// dolt database. Returns the beadsDir path.
func setupLifecycleTestDir(t *testing.T) string {
	t.Helper()
	doltBin := integration.RequireDolt(t)

	tmpDir := t.TempDir()
	configureDoltTestIdentity(t, doltBin, tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0700); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	cmd := exec.Command(doltBin, "init")
	cmd.Dir = doltDir
	cmd.Env = doltTestEnv(tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %v\n%s", err, out)
	}

	// Ensure no shared server mode interference.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")

	return beadsDir
}

func configureDoltTestIdentity(t *testing.T, doltBin, home string) {
	t.Helper()

	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "beads-test"},
		{"config", "--global", "--add", "user.email", "beads@test"},
	} {
		cmd := exec.Command(doltBin, args...)
		cmd.Env = doltTestEnv(home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt %v failed: %v\n%s", args, err, out)
		}
	}
}

func doltTestEnv(home string) []string {
	return append(os.Environ(), "HOME="+home, "DOLT_ROOT_PATH="+home)
}

// connectMySQL opens a MySQL connection to the dolt server.
// Caller is responsible for closing the returned *sql.DB.
func connectMySQL(t *testing.T, port int) *sql.DB {
	t.Helper()
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: "dolt"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	return db
}

func waitForPortClosed(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server port %d still accepting connections after %v", port, timeout)
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

	// Verify the server is no longer accepting connections. Detached child
	// PIDs can remain briefly observable as zombies after the listener exits.
	waitForPortClosed(t, state.Port, 5*time.Second)

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

// TestLifecycle_IdleStability verifies the dolt sql-server survives an idle
// period of 90 seconds without crashing. This directly reproduces the field
// report where dolt 2.0.3 on Windows silently crashes during idle (event
// scheduler or auto-GC background job kills the process).
//
// If this test fails sporadically (server found dead during idle wait), it
// confirms the background-worker crash hypothesis. Skip with -short for CI
// where 90s idle is too long. See: gh-3874-dolt-idle-crash.
func TestLifecycle_IdleStability(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}
	t.Logf("Server PID=%d, waiting 90s for idle stability", state.PID)

	// Verify server is alive at intervals during idle
	const idleDuration = 90 * time.Second
	const checkInterval = 15 * time.Second
	deadline := time.Now().Add(idleDuration)

	// Connect once at the start so we have a live SQL connection
	db := connectMySQL(t, state.Port)
	defer db.Close()

	var lastCheck time.Time
	for time.Now().Before(deadline) {
		time.Sleep(checkInterval)
		lastCheck = time.Now()

		// Verify OS process alive
		if !integration.IsProcessAlive(state.PID) {
			t.Fatalf("dolt process (PID %d) died during idle at %v (elapsed %v)",
				state.PID, lastCheck, lastCheck.Sub(time.Now().Add(-idleDuration)))
		}

		// Verify SQL engine alive via existing connection
		var one int
		if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
			t.Fatalf("SQL ping failed at %v (elapsed %v): %v — "+
				"SQL engine crashed while OS process survived",
				lastCheck, lastCheck.Sub(time.Now().Add(-idleDuration)), err)
		}
		if one != 1 {
			t.Errorf("SELECT 1 returned %d, expected 1", one)
		}
	}
	t.Logf("Server survived %v idle with %d SQL checks",
		idleDuration, int(idleDuration/checkInterval))

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestLifecycle_ConnectionBurstSurvival verifies the dolt server survives a
// burst of rapid TCP connect+disconnect cycles, simulating what waitForReady
// does during startup (up to 20 Dial+Close in 10s).
//
// The field report shows "Cannot send HandshakeV10 packet: connection aborted"
// errors in the log when beads connects and immediately closes TCP connections.
// Dolt interprets each Close()'s TCP RST as an aborted MySQL handshake.
// After ~60 such events over ~60 seconds, dolt's SQL engine silently dies.
//
// This test sends 100 rapid connect+close cycles and verifies the server stays
// alive and processes SQL normally after. See: gh-3875-wait-ready-rst-flood.
func TestLifecycle_ConnectionBurstSurvival(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}
	t.Logf("Server PID=%d on port %d", state.PID, state.Port)

	// Phase 1: Send 100 rapid TCP connect+close cycles (simulating waitForReady + breaker probes)
	const burstSize = 100
	burstStart := time.Now()
	for i := 0; i < burstSize; i++ {
		conn, dialErr := net.DialTimeout("tcp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Port)),
			500*time.Millisecond)
		if dialErr != nil {
			t.Fatalf("TCP dial %d/%d failed: %v", i+1, burstSize, dialErr)
		}
		// Read a few bytes so dolt starts the MySQL handshake
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		buf := make([]byte, 4)
		_, _ = conn.Read(buf)
		_ = conn.Close()
		// Small delay so dolt has time to process each connection
		time.Sleep(10 * time.Millisecond)
	}
	burstElapsed := time.Since(burstStart)
	t.Logf("Sent %d TCP connect+close cycles in %v (avg %v per cycle)",
		burstSize, burstElapsed, burstElapsed/burstSize)

	// Verify OS process survived
	if !integration.IsProcessAlive(state.PID) {
		t.Fatalf("Server process died during connection burst")
	}

	// Verify SQL engine survived
	db := connectMySQL(t, state.Port)
	defer db.Close()
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("SQL engine dead after connection burst: %v", err)
	}
	if one != 1 {
		t.Errorf("SELECT 1 returned %d, expected 1", one)
	}
	t.Log("SQL engine healthy after connection burst")

	// Phase 2: Verify the server still works for real operations
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS burst_test (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE after burst: %v", err)
	}
	if _, err := db.Exec("INSERT INTO burst_test VALUES (1), (2), (3)"); err != nil {
		t.Fatalf("INSERT after burst: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM burst_test").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT after burst: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 rows, got %d", count)
	}
	t.Log("Server functional after connection burst")

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestLifecycle_CombinedTCPProbeSurvival combines the two real crash triggers
// from the field report: 100 burst connections followed by 90s idle wait.
// This tests the cumulative effect: many RST-provoking connections weaken
// the server, then idle background workers finish it off.
func TestLifecycle_CombinedTCPProbeSurvival(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}
	t.Logf("Server PID=%d on port %d", state.PID, state.Port)

	// Phase 1: Connection burst
	const burstSize = 100
	for i := 0; i < burstSize; i++ {
		conn, dialErr := net.DialTimeout("tcp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Port)),
			500*time.Millisecond)
		if dialErr != nil {
			t.Fatalf("TCP dial %d/%d failed: %v", i+1, burstSize, dialErr)
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		buf := make([]byte, 4)
		_, _ = conn.Read(buf)
		_ = conn.Close()
		time.Sleep(5 * time.Millisecond)
	}
	t.Log("Phase 1: connection burst complete")

	// Verify server survived burst
	if !integration.IsProcessAlive(state.PID) {
		t.Fatal("Server died during connection burst")
	}

	// Phase 2: 60s idle wait
	// Only run idle wait when not -short
	if !testing.Short() {
		const idleDuration = 60 * time.Second
		db := connectMySQL(t, state.Port)
		defer db.Close()

		for remaining := idleDuration; remaining > 0; remaining -= 15 * time.Second {
			time.Sleep(15 * time.Second)
			if !integration.IsProcessAlive(state.PID) {
				t.Fatalf("Server died during idle at %v remaining", remaining)
			}
			var one int
			if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
				t.Fatalf("SQL engine died during idle: %v", err)
			}
		}
		t.Log("Phase 2: idle wait complete")
	}

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
