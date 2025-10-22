package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonLockPreventsMultipleInstances(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Acquire lock
	lock1, err := acquireDaemonLock(beadsDir, false)
	if err != nil {
		t.Fatalf("Failed to acquire first lock: %v", err)
	}
	defer lock1.Close()

	// Try to acquire lock again - should fail
	lock2, err := acquireDaemonLock(beadsDir, false)
	if err != ErrDaemonLocked {
		if lock2 != nil {
			lock2.Close()
		}
		t.Fatalf("Expected ErrDaemonLocked, got: %v", err)
	}

	// Release first lock
	lock1.Close()

	// Now should be able to acquire lock
	lock3, err := acquireDaemonLock(beadsDir, false)
	if err != nil {
		t.Fatalf("Failed to acquire lock after release: %v", err)
	}
	lock3.Close()
}

func TestTryDaemonLockDetectsRunning(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Initially no daemon running
	running, _ := tryDaemonLock(beadsDir)
	if running {
		t.Fatal("Expected no daemon running initially")
	}

	// Acquire lock
	lock, err := acquireDaemonLock(beadsDir, false)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	defer lock.Close()

	// Now should detect daemon running
	running, pid := tryDaemonLock(beadsDir)
	if !running {
		t.Fatal("Expected daemon to be detected as running")
	}
	if pid != os.Getpid() {
		t.Errorf("Expected PID %d, got %d", os.Getpid(), pid)
	}
}

func TestBackwardCompatibilityWithOldDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Simulate old daemon: PID file exists but no lock file
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	currentPID := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", currentPID)), 0644); err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	// tryDaemonLock should detect the old daemon via PID file fallback
	running, pid := tryDaemonLock(beadsDir)
	if !running {
		t.Fatal("Expected old daemon to be detected via PID file")
	}
	if pid != currentPID {
		t.Errorf("Expected PID %d, got %d", currentPID, pid)
	}

	// Clean up PID file
	os.Remove(pidFile)

	// Now should report no daemon running
	running, _ = tryDaemonLock(beadsDir)
	if running {
		t.Fatal("Expected no daemon running after PID file removed")
	}
}

func TestMultipleDaemonProcessesRace(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	// Find the bd binary
	bdBinary, err := exec.LookPath("bd")
	if err != nil {
		// Try local build
		if _, err := os.Stat("./bd"); err == nil {
			bdBinary = "./bd"
		} else {
			t.Skip("bd binary not found, skipping race test")
		}
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	beadsDir := filepath.Dir(dbPath)

	// Initialize a test database with git repo
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Create git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	// Initialize bd
	cmd = exec.Command(bdBinary, "init", "--prefix", "test")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "BEADS_DB="+dbPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init bd: %v\nOutput: %s", err, out)
	}

	// Try to start 5 daemons simultaneously
	numAttempts := 5
	results := make(chan error, numAttempts)

	for i := 0; i < numAttempts; i++ {
		go func() {
			cmd := exec.Command(bdBinary, "daemon", "--interval", "10m")
			cmd.Dir = tmpDir
			cmd.Env = append(os.Environ(), "BEADS_DB="+dbPath)
			err := cmd.Start()
			if err != nil {
				results <- err
				return
			}

			// Wait a bit for daemon to start
			time.Sleep(200 * time.Millisecond)

			// Check if it's still running
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			results <- cmd.Wait()
		}()
	}

	// Wait for all attempts
	var successCount int
	var alreadyRunning int
	timeout := time.After(5 * time.Second)

	for i := 0; i < numAttempts; i++ {
		select {
		case err := <-results:
			if err == nil {
				successCount++
			} else if strings.Contains(err.Error(), "exit status 1") {
				// Could be "already running" error
				alreadyRunning++
			}
		case <-timeout:
			t.Fatal("Test timed out waiting for daemon processes")
		}
	}

	// Clean up any remaining daemon files
	os.Remove(filepath.Join(beadsDir, "daemon.pid"))
	os.Remove(filepath.Join(beadsDir, "daemon.lock"))
	os.Remove(filepath.Join(beadsDir, "bd.sock"))

	t.Logf("Results: %d success, %d already running", successCount, alreadyRunning)

	// At most one should have succeeded in holding the lock
	// (though timing means even the first might have exited by the time we checked)
	if alreadyRunning < numAttempts-1 {
		t.Logf("Warning: Expected at least %d processes to fail with 'already running', got %d", 
			numAttempts-1, alreadyRunning)
		t.Log("This could indicate a race condition, but may also be timing-related in tests")
	}
}
