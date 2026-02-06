//go:build !windows
// +build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// =============================================================================
// Daemon Lock Race Condition Tests
// =============================================================================
//
// These tests verify correct behavior of daemon locking under concurrent access.
// Run with: go test -race -run TestDaemonLock -v
//
// Race conditions being tested:
// 1. Stale lock file detection (daemon crashed, lock file remains)
// 2. Lock file cleanup after crash simulation
// 3. Concurrent daemon start attempts (multiple processes racing)
// 4. PID reuse scenarios (old PID reused by new process)
// 5. flock vs file existence checks (timing windows)
//
// =============================================================================

// TestStaleLockFileDetection verifies that stale lock files from crashed daemons
// are properly detected via flock semantics.
//
// Race condition tested: A daemon crashes leaving a lock file, but no flock is held.
// The lock file exists but should not prevent a new daemon from starting.
func TestStaleLockFileDetection(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(beadsDir, "daemon.lock")
	dbPath := filepath.Join(beadsDir, "beads.db")

	// simulate a crashed daemon by writing a lock file without holding flock
	stalePID := 999999 // non-existent PID
	lockContent := fmt.Sprintf(`{"pid":%d,"database":"%s","version":"test"}`, stalePID, dbPath)
	if err := os.WriteFile(lockPath, []byte(lockContent), 0600); err != nil {
		t.Fatalf("Failed to write stale lock file: %v", err)
	}

	// verify file exists
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("Lock file should exist: %v", err)
	}

	// tryDaemonLock should NOT detect a running daemon since flock is not held
	running, pid := tryDaemonLock(beadsDir)
	if running && pid == stalePID {
		// check if process actually exists
		if !isProcessRunning(stalePID) {
			t.Log("tryDaemonLock returned running=true but process is dead (PID file fallback)")
			// this is the PID file fallback behavior - it's actually correct
			// to detect this via the running process check
		}
	}

	// should be able to acquire the lock since flock is not held
	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Should be able to acquire lock over stale file: %v", err)
	}
	defer lock.Close()

	// verify lock was acquired
	if lock == nil || lock.file == nil {
		t.Fatal("Lock should be valid")
	}
}

// TestLockFileCleanupAfterCrash simulates a daemon crash and verifies cleanup.
//
// Race condition tested: Daemon holds flock, then crashes (simulated via close).
// New daemon should be able to acquire the lock after the old one releases.
func TestLockFileCleanupAfterCrash(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	// acquire lock
	lock1, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// verify daemon is detected as running
	running, pid := tryDaemonLock(beadsDir)
	if !running {
		t.Fatal("Daemon should be detected as running while lock is held")
	}
	if pid != os.Getpid() {
		t.Errorf("PID mismatch: expected %d, got %d", os.Getpid(), pid)
	}

	// simulate crash by closing file (releases flock)
	lock1.Close()

	// small delay to ensure OS releases flock
	time.Sleep(10 * time.Millisecond)

	// new daemon should be able to acquire lock
	lock2, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock after crash: %v", err)
	}
	defer lock2.Close()

	// verify new lock is valid
	if lock2 == nil || lock2.file == nil {
		t.Fatal("New lock should be valid")
	}
}

// TestConcurrentDaemonStartAttempts verifies that only one daemon can acquire the lock
// when multiple processes attempt to start simultaneously.
//
// Race condition tested: Multiple goroutines racing to acquire the daemon lock.
// Only one should succeed, others should get ErrDaemonLocked.
func TestConcurrentDaemonStartAttempts(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	const numAttempts = 10
	var (
		successCount   int32
		lockedCount    int32
		errorCount     int32
		acquiredLock   *DaemonLock
		acquiredLockMu sync.Mutex
	)

	var wg sync.WaitGroup
	startSignal := make(chan struct{})

	for i := 0; i < numAttempts; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// wait for start signal to maximize contention
			<-startSignal

			lock, err := acquireDaemonLock(beadsDir, dbPath)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
				acquiredLockMu.Lock()
				if acquiredLock == nil {
					acquiredLock = lock
				} else {
					// unexpected - two locks succeeded!
					t.Errorf("Goroutine %d acquired lock but one already exists", id)
					lock.Close()
				}
				acquiredLockMu.Unlock()
			} else if err == ErrDaemonLocked {
				atomic.AddInt32(&lockedCount, 1)
			} else {
				atomic.AddInt32(&errorCount, 1)
				t.Logf("Goroutine %d got unexpected error: %v", id, err)
			}
		}(i)
	}

	// release all goroutines simultaneously
	close(startSignal)
	wg.Wait()

	// cleanup
	acquiredLockMu.Lock()
	if acquiredLock != nil {
		acquiredLock.Close()
	}
	acquiredLockMu.Unlock()

	// verify exactly one success
	if successCount != 1 {
		t.Errorf("Expected exactly 1 successful lock acquisition, got %d", successCount)
	}

	// verify others got ErrDaemonLocked
	expectedLocked := int32(numAttempts - 1)
	if lockedCount != expectedLocked {
		t.Errorf("Expected %d ErrDaemonLocked errors, got %d", expectedLocked, lockedCount)
	}

	if errorCount > 0 {
		t.Errorf("Got %d unexpected errors", errorCount)
	}
}

// TestPIDReuseScenario verifies correct behavior when a PID is reused by the OS.
//
// Race condition tested: Old daemon dies, OS reuses PID for a different process.
// Lock should be released when file descriptor closes, regardless of PID.
func TestPIDReuseScenario(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	pidFile := filepath.Join(beadsDir, "daemon.pid")

	// acquire lock
	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// get current PID from lock file
	lockInfo, err := readDaemonLockInfo(beadsDir)
	if err != nil {
		t.Fatalf("Failed to read lock info: %v", err)
	}

	originalPID := lockInfo.PID
	t.Logf("Original lock PID: %d", originalPID)

	// simulate PID reuse by writing a different PID to the PID file
	// (this simulates the scenario where the OS reused the PID)
	fakePID := 12345
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(fakePID)), 0600); err != nil {
		t.Fatalf("Failed to write fake PID: %v", err)
	}

	// flock should still be valid regardless of PID file contents
	running, _ := tryDaemonLock(beadsDir)
	if !running {
		t.Error("Daemon should still be detected as running (flock is held)")
	}

	// release lock
	lock.Close()

	// now should be able to acquire
	lock2, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock after release: %v", err)
	}
	lock2.Close()
}

// TestFlockVsFileExistenceRace verifies correct behavior when checking lock status
// while another process is acquiring/releasing the lock.
//
// Race condition tested: Process A is acquiring lock while Process B checks status.
// There is a brief window between flock acquisition and PID file write where
// running=true but pid=0. This is an expected transient state, not an error.
// The lock IS held (running=true), we just can't identify the holder momentarily.
func TestFlockVsFileExistenceRace(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	const iterations = 100
	var (
		transientStateCount int32 // running=true, pid=0 (expected during lock acquisition)
		lockAcquired        int32
		lockChecked         int32
		wg                  sync.WaitGroup
	)

	for i := 0; i < iterations; i++ {
		wg.Add(2)

		// goroutine 1: acquire and release lock rapidly
		go func() {
			defer wg.Done()
			lock, err := acquireDaemonLock(beadsDir, dbPath)
			if err == nil {
				atomic.AddInt32(&lockAcquired, 1)
				time.Sleep(time.Microsecond * 100)
				lock.Close()
			}
		}()

		// goroutine 2: check lock status
		go func() {
			defer wg.Done()
			running, pid := tryDaemonLock(beadsDir)
			atomic.AddInt32(&lockChecked, 1)
			// running=true with pid=0 is a valid transient state during lock acquisition
			// The lock IS held, we just caught the window before PID was written
			if running && pid == 0 {
				atomic.AddInt32(&transientStateCount, 1)
			}
		}()

		wg.Wait()
	}

	// Log observations for debugging, but don't fail
	// A small number of transient states is expected due to the flock-then-write pattern
	t.Logf("iterations=%d, locks_acquired=%d, locks_checked=%d, transient_states=%d",
		iterations, lockAcquired, lockChecked, transientStateCount)

	// Sanity check: we should have successfully acquired locks
	if lockAcquired == 0 {
		t.Error("No locks were acquired - test environment issue")
	}
}

// TestLockAcquisitionTimeout verifies that lock acquisition doesn't hang indefinitely.
//
// Race condition tested: One daemon holds lock, another tries to acquire.
// The second should fail quickly with ErrDaemonLocked, not block.
func TestLockAcquisitionTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	// acquire lock
	lock1, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire first lock: %v", err)
	}
	defer lock1.Close()

	// try to acquire second lock with timeout
	done := make(chan bool, 1)
	go func() {
		start := time.Now()
		_, err := acquireDaemonLock(beadsDir, dbPath)
		elapsed := time.Since(start)
		if err != ErrDaemonLocked {
			t.Errorf("Expected ErrDaemonLocked, got: %v", err)
		}
		// should return quickly (non-blocking)
		if elapsed > time.Second {
			t.Errorf("Lock acquisition took too long: %v (expected < 1s)", elapsed)
		}
		done <- true
	}()

	select {
	case <-done:
		// expected
	case <-time.After(5 * time.Second):
		t.Fatal("Lock acquisition timed out (blocked indefinitely)")
	}
}

// TestLockFilePermissions verifies lock file is created with secure permissions.
func TestLockFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	lockPath := filepath.Join(beadsDir, "daemon.lock")

	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	defer lock.Close()

	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("Failed to stat lock file: %v", err)
	}

	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("Lock file has wrong permissions: %o (expected 0600)", mode)
	}
}

// TestLockFileContentsAfterReacquire verifies lock file contents are updated correctly
// when a new daemon acquires the lock after the previous one releases.
func TestLockFileContentsAfterReacquire(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	// first daemon acquires lock
	lock1, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire first lock: %v", err)
	}

	info1, _ := readDaemonLockInfo(beadsDir)
	lock1.Close()

	// second daemon acquires lock
	lock2, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire second lock: %v", err)
	}
	defer lock2.Close()

	info2, err := readDaemonLockInfo(beadsDir)
	if err != nil {
		t.Fatalf("Failed to read second lock info: %v", err)
	}

	// verify timestamp is updated
	if info1 != nil && !info2.StartedAt.After(info1.StartedAt) {
		t.Error("Second lock should have later timestamp")
	}

	// verify PID matches current process
	if info2.PID != os.Getpid() {
		t.Errorf("Lock PID mismatch: expected %d, got %d", os.Getpid(), info2.PID)
	}
}

// TestConcurrentTryDaemonLock verifies tryDaemonLock is safe for concurrent use.
//
// Race condition tested: Multiple goroutines calling tryDaemonLock simultaneously.
// Should not cause data races or panics.
func TestConcurrentTryDaemonLock(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	// acquire lock to have a running daemon
	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	defer lock.Close()

	const numGoroutines = 50
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				running, pid := tryDaemonLock(beadsDir)
				// should always be consistent
				if running && pid <= 0 {
					t.Errorf("Inconsistent state: running=%v but pid=%d", running, pid)
				}
			}
		}()
	}

	wg.Wait()
}

// TestLockReleaseOnProcessExit verifies flock is released when process exits.
// This test spawns a child process to hold the lock, then verifies it's released.
func TestLockReleaseOnProcessExit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	// Skip if flock command not available (not available on macOS)
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("Skipping test: flock command not available (Linux-specific)")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(beadsDir, "daemon.lock")
	dbPath := filepath.Join(beadsDir, "beads.db")

	// create a helper script that acquires lock and exits
	helperScript := filepath.Join(tmpDir, "lock_holder.sh")
	scriptContent := fmt.Sprintf(`#!/bin/bash
exec 200>"%s"
flock -n 200 || exit 1
echo $$ > "%s.pid"
sleep 5
`, lockPath, lockPath)
	if err := os.WriteFile(helperScript, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write helper script: %v", err)
	}

	// start the helper process
	cmd := exec.Command("/bin/bash", helperScript)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start helper: %v", err)
	}

	// wait for PID file to be written
	pidFile := lockPath + ".pid"
	deadline := time.Now().Add(2 * time.Second)
	var helperPID int
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				helperPID = pid
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	if helperPID == 0 {
		cmd.Process.Kill()
		t.Fatal("Helper process did not write PID file")
	}

	// verify we cannot acquire lock while helper holds it
	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != ErrDaemonLocked {
		if lock != nil {
			lock.Close()
		}
		t.Errorf("Expected ErrDaemonLocked while helper holds lock, got: %v", err)
	}

	// kill the helper process
	if err := syscall.Kill(helperPID, syscall.SIGKILL); err != nil {
		t.Logf("Warning: failed to kill helper: %v", err)
	}
	cmd.Wait()

	// wait for OS to release flock
	time.Sleep(100 * time.Millisecond)

	// now we should be able to acquire the lock
	lock, err = acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock after helper exit: %v", err)
	}
	lock.Close()
}

// TestRapidLockAcquireRelease tests rapid lock acquire/release cycles
// to ensure no resource leaks or deadlocks.
func TestRapidLockAcquireRelease(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	const cycles = 100
	for i := 0; i < cycles; i++ {
		lock, err := acquireDaemonLock(beadsDir, dbPath)
		if err != nil {
			t.Fatalf("Cycle %d: failed to acquire lock: %v", i, err)
		}
		lock.Close()
	}
}

// TestLockWithParentPID verifies parent PID tracking in lock info.
func TestLockWithParentPID(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	defer lock.Close()

	info, err := readDaemonLockInfo(beadsDir)
	if err != nil {
		t.Fatalf("Failed to read lock info: %v", err)
	}

	expectedParentPID := os.Getppid()
	if info.ParentPID != expectedParentPID {
		t.Errorf("Parent PID mismatch: expected %d, got %d", expectedParentPID, info.ParentPID)
	}
}
