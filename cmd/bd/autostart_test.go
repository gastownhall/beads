package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	
	"github.com/steveyegge/beads/internal/config"
)

func TestDaemonAutoStart(t *testing.T) {
	// Initialize config for tests
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}
	
	// Save original env
	origAutoStart := os.Getenv("BEADS_AUTO_START_DAEMON")
	origNoDaemon := os.Getenv("BEADS_NO_DAEMON")
	defer func() {
		if origAutoStart != "" {
			os.Setenv("BEADS_AUTO_START_DAEMON", origAutoStart)
		} else {
			os.Unsetenv("BEADS_AUTO_START_DAEMON")
		}
		if origNoDaemon != "" {
			os.Setenv("BEADS_NO_DAEMON", origNoDaemon)
		} else {
			os.Unsetenv("BEADS_NO_DAEMON")
		}
	}()

	// Ensure BEADS_NO_DAEMON doesn't interfere with these tests
	os.Unsetenv("BEADS_NO_DAEMON")

	t.Run("shouldAutoStartDaemon defaults to true", func(t *testing.T) {
		os.Unsetenv("BEADS_AUTO_START_DAEMON")
		if !shouldAutoStartDaemon() {
			t.Error("Expected auto-start to be enabled by default")
		}
	})

	t.Run("shouldAutoStartDaemon respects false", func(t *testing.T) {
		os.Setenv("BEADS_AUTO_START_DAEMON", "false")
		if shouldAutoStartDaemon() {
			t.Error("Expected auto-start to be disabled when set to 'false'")
		}
	})

	t.Run("shouldAutoStartDaemon respects 0", func(t *testing.T) {
		os.Setenv("BEADS_AUTO_START_DAEMON", "0")
		if shouldAutoStartDaemon() {
			t.Error("Expected auto-start to be disabled when set to '0'")
		}
	})

	t.Run("shouldAutoStartDaemon respects no", func(t *testing.T) {
		os.Setenv("BEADS_AUTO_START_DAEMON", "no")
		if shouldAutoStartDaemon() {
			t.Error("Expected auto-start to be disabled when set to 'no'")
		}
	})

	t.Run("shouldAutoStartDaemon respects off", func(t *testing.T) {
		os.Setenv("BEADS_AUTO_START_DAEMON", "off")
		if shouldAutoStartDaemon() {
			t.Error("Expected auto-start to be disabled when set to 'off'")
		}
	})

	t.Run("shouldAutoStartDaemon handles case and whitespace", func(t *testing.T) {
		os.Setenv("BEADS_AUTO_START_DAEMON", "  FALSE  ")
		if shouldAutoStartDaemon() {
			t.Error("Expected auto-start to be disabled when set to '  FALSE  '")
		}
	})

	t.Run("shouldAutoStartDaemon respects true", func(t *testing.T) {
		os.Setenv("BEADS_AUTO_START_DAEMON", "true")
		if !shouldAutoStartDaemon() {
			t.Error("Expected auto-start to be enabled when set to 'true'")
		}
	})
}

func TestDaemonStartFailureTracking(t *testing.T) {
	// Reset failure state
	daemonStartFailures = 0
	lastDaemonStartAttempt = time.Time{}

	t.Run("canRetryDaemonStart allows first attempt", func(t *testing.T) {
		if !canRetryDaemonStart() {
			t.Error("Expected first attempt to be allowed")
		}
	})

	t.Run("exponential backoff after failures", func(t *testing.T) {
		// Simulate first failure
		recordDaemonStartFailure()
		if daemonStartFailures != 1 {
			t.Errorf("Expected failure count 1, got %d", daemonStartFailures)
		}

		// Should not allow immediate retry
		if canRetryDaemonStart() {
			t.Error("Expected retry to be blocked immediately after failure")
		}

		// Wait for backoff period (5 seconds for first failure)
		lastDaemonStartAttempt = time.Now().Add(-6 * time.Second)
		if !canRetryDaemonStart() {
			t.Error("Expected retry to be allowed after backoff period")
		}

		// Simulate second failure
		recordDaemonStartFailure()
		if daemonStartFailures != 2 {
			t.Errorf("Expected failure count 2, got %d", daemonStartFailures)
		}

		// Should not allow immediate retry (10 second backoff)
		if canRetryDaemonStart() {
			t.Error("Expected retry to be blocked immediately after second failure")
		}

		// Wait for longer backoff
		lastDaemonStartAttempt = time.Now().Add(-11 * time.Second)
		if !canRetryDaemonStart() {
			t.Error("Expected retry to be allowed after longer backoff period")
		}
	})

	t.Run("exponential backoff durations are correct", func(t *testing.T) {
		testCases := []struct {
			failures int
			expected time.Duration
		}{
			{1, 5 * time.Second},
			{2, 10 * time.Second},
			{3, 20 * time.Second},
			{4, 40 * time.Second},
			{5, 80 * time.Second},
			{6, 120 * time.Second},  // Capped
			{10, 120 * time.Second}, // Still capped
		}

		for _, tc := range testCases {
			daemonStartFailures = tc.failures
			lastDaemonStartAttempt = time.Now()

			// Should not allow retry immediately
			if canRetryDaemonStart() {
				t.Errorf("Failures=%d: Expected immediate retry to be blocked", tc.failures)
			}

			// Should allow retry after expected duration
			lastDaemonStartAttempt = time.Now().Add(-(tc.expected + time.Second))
			if !canRetryDaemonStart() {
				t.Errorf("Failures=%d: Expected retry after %v", tc.failures, tc.expected)
			}
		}
	})

	t.Run("recordDaemonStartSuccess resets failures", func(t *testing.T) {
		daemonStartFailures = 10
		recordDaemonStartSuccess()
		if daemonStartFailures != 0 {
			t.Errorf("Expected failure count to reset to 0, got %d", daemonStartFailures)
		}
	})

	// Reset state
	daemonStartFailures = 0
	lastDaemonStartAttempt = time.Time{}
}

func TestGetSocketPath(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}

	// Set dbPath to temp location
	originalDbPath := dbPath
	dbPath = filepath.Join(beadsDir, "test.db")
	defer func() { dbPath = originalDbPath }()

	t.Run("prefers local socket when it exists", func(t *testing.T) {
		localSocket := filepath.Join(beadsDir, "bd.sock")

		// Create local socket file
		if err := os.WriteFile(localSocket, []byte{}, 0600); err != nil {
			t.Fatalf("Failed to create socket file: %v", err)
		}
		defer os.Remove(localSocket)

		socketPath := getSocketPath()
		if socketPath != localSocket {
			t.Errorf("Expected local socket %s, got %s", localSocket, socketPath)
		}
	})

	t.Run("always returns local socket path", func(t *testing.T) {
		// Ensure no local socket exists
		localSocket := filepath.Join(beadsDir, "bd.sock")
		os.Remove(localSocket)

		// Create a fake global socket in temp directory instead of home dir
		// This avoids issues in sandboxed build environments
		fakeHome := t.TempDir()
		globalBeadsDir := filepath.Join(fakeHome, ".beads")
		if err := os.MkdirAll(globalBeadsDir, 0755); err != nil {
			t.Fatalf("Failed to create fake global beads directory: %v", err)
		}
		globalSocket := filepath.Join(globalBeadsDir, "bd.sock")

		if err := os.WriteFile(globalSocket, []byte{}, 0600); err != nil {
			t.Fatalf("Failed to create fake global socket file: %v", err)
		}

		// Note: This test verifies that getSocketPath() returns the local socket
		// even when a global socket might exist. We can't actually test the real
		// global socket behavior in sandboxed environments, but the function
		// logic is still validated.
		socketPath := getSocketPath()
		if socketPath != localSocket {
			t.Errorf("Expected local socket %s, got %s", localSocket, socketPath)
		}
	})

	t.Run("defaults to local socket when none exist", func(t *testing.T) {
		// Ensure no local socket exists
		localSocket := filepath.Join(beadsDir, "bd.sock")
		os.Remove(localSocket)

		// We can't remove the global socket in sandboxed environments,
		// but the test still validates that getSocketPath() returns the
		// local socket path as expected
		socketPath := getSocketPath()
		if socketPath != localSocket {
			t.Errorf("Expected default to local socket %s, got %s", localSocket, socketPath)
		}
	})
}
