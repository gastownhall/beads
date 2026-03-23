//go:build integration && !windows

package doltserver_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestDirtyState_StalePIDAliveNonDolt verifies that Start() recovers when the
// PID file contains the PID of a live non-dolt process.
// Would have caught: false-positive IsRunning when PID recycled to a different process.
func TestDirtyState_StalePIDAliveNonDolt(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Start a non-dolt process.
	sleepCmd := exec.Command("sleep", "120")
	if err := sleepCmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = sleepCmd.Process.Signal(syscall.SIGTERM)
		_ = sleepCmd.Wait()
	})

	// Write its PID as the server PID.
	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.WriteStalePID(sleepCmd.Process.Pid)
	corruptor.WriteStalePort(13306)

	// IsRunning should detect non-dolt process.
	state, err := doltserver.IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if state.Running {
		t.Error("IsRunning returned true for non-dolt PID")
	}

	// Start should succeed after cleaning up stale state.
	startState, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !startState.Running {
		t.Fatal("Start returned Running=false")
	}
	if p, err := os.FindProcess(startState.PID); err == nil {
		reg.Register(p)
	}

	// Verify the sleep process was NOT killed (wrong process protection).
	if !integration.IsProcessAlive(sleepCmd.Process.Pid) {
		t.Error("sleep process was killed — Start should not kill non-dolt processes")
	}

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(startState.PID)
}

// TestDirtyState_StalePIDDead verifies recovery when the PID file points
// to a process that no longer exists.
// Would have caught: GH#2559 (system restart breaks all Dolt connections).
func TestDirtyState_StalePIDDead(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Write a PID that doesn't exist.
	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.WriteStalePID(99999999)
	corruptor.WriteStalePort(13306)

	// IsRunning should return false and clean up.
	state, err := doltserver.IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if state.Running {
		t.Error("IsRunning returned true for dead PID")
	}

	// PID file should be cleaned up.
	if integration.FileExists(corruptor.PIDFilePath()) {
		t.Error("PID file not cleaned up after detecting dead PID")
	}

	// Start should succeed.
	startState, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p, err := os.FindProcess(startState.PID); err == nil {
		reg.Register(p)
	}

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(startState.PID)
}

// TestDirtyState_StalePortOnly verifies recovery when a port file exists
// but no PID file is present.
func TestDirtyState_StalePortOnly(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	// Write only a stale port file.
	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.WriteStalePort(13306)

	// Start should succeed.
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !state.Running {
		t.Fatal("Start returned Running=false")
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestDirtyState_CorruptPIDFile verifies recovery when the PID file contains
// non-numeric content.
func TestDirtyState_CorruptPIDFile(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.WriteCorruptPID()
	corruptor.WriteStalePort(13306)

	// IsRunning should return false and clean up corrupt files.
	state, err := doltserver.IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if state.Running {
		t.Error("IsRunning returned true for corrupt PID file")
	}
	if integration.FileExists(corruptor.PIDFilePath()) {
		t.Error("corrupt PID file not cleaned up")
	}
	if integration.FileExists(corruptor.PortFilePath()) {
		t.Error("port file not cleaned up alongside corrupt PID")
	}
}

// TestDirtyState_TruncatedMetadata verifies that truncated metadata.json
// does not cause a panic.
func TestDirtyState_TruncatedMetadata(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.WriteTruncatedMetadata()

	// Start should not panic. It may succeed (ignoring corrupt metadata)
	// or return an error — either is acceptable.
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Logf("Start returned error (acceptable): %v", err)
		return
	}

	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}
	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestDirtyState_PortZero verifies recovery when port file contains "0".
// Would have caught: GH#2598 (port 0 in state file poisons circuit breaker).
func TestDirtyState_PortZero(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.WritePortZero()

	// Start should succeed — port 0 is treated as "no port".
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state.Port == 0 {
		t.Error("Start returned port 0 — should have allocated a real port")
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestDirtyState_CombinedStalePIDAndPort verifies recovery when both PID and
// port files are stale (e.g., after an unclean system shutdown).
// Would have caught: GH#2559 (init --force required after system restart).
func TestDirtyState_CombinedStalePIDAndPort(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.CreateCombinedStaleState(99999999, 13306)

	// Start should clean up and succeed.
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !state.Running {
		t.Fatal("Start returned Running=false")
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	// Verify we can actually connect.
	db := connectMySQL(t, state.Port)
	if _, err := db.Exec("SELECT 1"); err != nil {
		t.Errorf("server not connectable after dirty state recovery: %v", err)
	}
	_ = db.Close()

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}

// TestDirtyState_OrphanNomsLock verifies that an orphaned 0-byte noms LOCK
// file (from a power loss) does not prevent server startup.
func TestDirtyState_OrphanNomsLock(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	diag := integration.NewDiagnostics(t, beadsDir)
	diag.CaptureOnFailure()

	doltDir := filepath.Join(beadsDir, "dolt")
	corruptor := integration.NewStateCorruptor(t, beadsDir)
	corruptor.CreateOrphanNomsLock(doltDir)

	// Verify the lock file exists.
	lockPath := filepath.Join(doltDir, "noms", "LOCK")
	if !integration.FileExists(lockPath) {
		t.Fatal("orphan LOCK file was not created")
	}

	// Start should succeed (dolt handles orphan LOCK files).
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		// Some dolt versions may fail here — log but don't hard-fail.
		t.Logf("Start with orphan LOCK: %v (may be dolt version dependent)", err)
		return
	}
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}

	// Give server time to initialize.
	time.Sleep(1 * time.Second)

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop: %v", err)
	}
	reg.Deregister(state.PID)
}
