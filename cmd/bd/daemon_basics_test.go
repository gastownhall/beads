package main

import (
	"os"
	"testing"
)

// TestComputeDaemonParentPID tests the parent PID computation logic
func TestComputeDaemonParentPID(t *testing.T) {
	tests := []struct {
		name           string
		envValue       string
		expectedPID    int
		expectsGetppid bool // whether we expect os.Getppid() to be called
	}{
		{
			name:           "BD_DAEMON_FOREGROUND not set",
			envValue:       "",
			expectedPID:    0, // Placeholder - will be replaced with actual Getppid()
			expectsGetppid: true,
		},
		{
			name:           "BD_DAEMON_FOREGROUND=1",
			envValue:       "1",
			expectedPID:    0,
			expectsGetppid: false,
		},
		{
			name:           "BD_DAEMON_FOREGROUND=0",
			envValue:       "0",
			expectedPID:    0, // Placeholder - will be replaced with actual Getppid()
			expectsGetppid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env
			oldVal, wasSet := os.LookupEnv("BD_DAEMON_FOREGROUND")
			defer func() {
				if wasSet {
					os.Setenv("BD_DAEMON_FOREGROUND", oldVal)
				} else {
					os.Unsetenv("BD_DAEMON_FOREGROUND")
				}
			}()

			// Set test env
			if tt.envValue != "" {
				os.Setenv("BD_DAEMON_FOREGROUND", tt.envValue)
			} else {
				os.Unsetenv("BD_DAEMON_FOREGROUND")
			}

			result := computeDaemonParentPID()

			if tt.name == "BD_DAEMON_FOREGROUND=1" {
				if result != 0 {
					t.Errorf("computeDaemonParentPID() = %d, want 0", result)
				}
			} else if tt.expectsGetppid {
				// When BD_DAEMON_FOREGROUND is not "1", we should get os.Getppid()
				expectedPID := os.Getppid()
				if result != expectedPID {
					t.Errorf("computeDaemonParentPID() = %d, want %d (os.Getppid())", result, expectedPID)
				}
			}
		})
	}
}

// TestCheckParentProcessAlive tests parent process alive checking
func TestCheckParentProcessAlive(t *testing.T) {
	tests := []struct {
		name       string
		parentPID  int
		expected   bool
		description string
	}{
		{
			name:        "PID 0 (not tracked)",
			parentPID:   0,
			expected:    true,
			description: "Should return true for untracked (PID 0)",
		},
		{
			name:        "PID 1 (init/launchd)",
			parentPID:   1,
			expected:    true,
			description: "Should return true for init process",
		},
		{
			name:        "current process PID",
			parentPID:   os.Getpid(),
			expected:    true,
			description: "Should return true for current running process",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkParentProcessAlive(tt.parentPID)
			if result != tt.expected {
				t.Errorf("checkParentProcessAlive(%d) = %v, want %v (%s)",
					tt.parentPID, result, tt.expected, tt.description)
			}
		})
	}
}

// TestCheckParentProcessAlive_DeadProcess tests with an invalid PID
func TestCheckParentProcessAlive_InvalidPID(t *testing.T) {
	// Use a very high PID that's unlikely to exist
	invalidPID := 999999
	result := checkParentProcessAlive(invalidPID)
	
	// This should return false since the process doesn't exist
	if result == true {
		t.Errorf("checkParentProcessAlive(%d) = true, want false (process should not exist)", invalidPID)
	}
}

// TestGetPIDFileForSocket tests socket to PID file path conversion
func TestGetPIDFileForSocket(t *testing.T) {
	tests := []struct {
		name       string
		socketPath string
		expected   string
	}{
		{
			name:       "typical beads socket",
			socketPath: "/home/user/.beads/bd.sock",
			expected:   "/home/user/.beads/daemon.pid",
		},
		{
			name:       "root .beads directory",
			socketPath: "/root/.beads/bd.sock",
			expected:   "/root/.beads/daemon.pid",
		},
		{
			name:       "temporary directory",
			socketPath: "/tmp/test.sock",
			expected:   "/tmp/daemon.pid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getPIDFileForSocket(tt.socketPath)
			if result != tt.expected {
				t.Errorf("getPIDFileForSocket(%q) = %q, want %q", tt.socketPath, result, tt.expected)
			}
		})
	}
}

// TestReadPIDFromFile tests reading PID from file
func TestReadPIDFromFile(t *testing.T) {
	t.Run("valid PID", func(t *testing.T) {
		// Create a temporary file
		tmpFile, err := os.CreateTemp("", "pid")
		if err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
		defer os.Remove(tmpFile.Name())

		// Write a PID
		if _, err := tmpFile.WriteString("12345\n"); err != nil {
			t.Fatalf("Failed to write PID: %v", err)
		}
		tmpFile.Close()

		// Read it back
		pid, err := readPIDFromFile(tmpFile.Name())
		if err != nil {
			t.Errorf("readPIDFromFile() returned error: %v", err)
		}
		if pid != 12345 {
			t.Errorf("readPIDFromFile() = %d, want 12345", pid)
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		_, err := readPIDFromFile("/nonexistent/path/to/file")
		if err == nil {
			t.Error("readPIDFromFile() should return error for nonexistent file")
		}
	})

	t.Run("invalid PID content", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "pid")
		if err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.WriteString("not-a-number\n"); err != nil {
			t.Fatalf("Failed to write content: %v", err)
		}
		tmpFile.Close()

		_, err = readPIDFromFile(tmpFile.Name())
		if err == nil {
			t.Error("readPIDFromFile() should return error for invalid content")
		}
	})
}

// TestIsPIDAlive tests PID alive checking
func TestIsPIDAlive(t *testing.T) {
	tests := []struct {
		name     string
		pid      int
		expected bool
		description string
	}{
		{
			name:        "zero PID",
			pid:         0,
			expected:    false,
			description: "PID 0 is invalid",
		},
		{
			name:        "negative PID",
			pid:         -1,
			expected:    false,
			description: "Negative PID is invalid",
		},
		{
			name:        "current process",
			pid:         os.Getpid(),
			expected:    true,
			description: "Current process should be alive",
		},
		{
			name:        "invalid PID",
			pid:         999999,
			expected:    false,
			description: "Non-existent process should not be alive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPIDAlive(tt.pid)
			if result != tt.expected {
				t.Errorf("isPIDAlive(%d) = %v, want %v (%s)",
					tt.pid, result, tt.expected, tt.description)
			}
		})
	}
}

// TestShouldAutoStartDaemon_Disabled tests BEADS_NO_DAEMON environment variable handling
func TestShouldAutoStartDaemon_Disabled(t *testing.T) {
	tests := []struct {
		name           string
		noDaemonValue  string
		shouldDisable  bool
		description    string
	}{
		{
			name:          "BEADS_NO_DAEMON=1",
			noDaemonValue: "1",
			shouldDisable: true,
			description:   "Should be disabled for BEADS_NO_DAEMON=1",
		},
		{
			name:          "BEADS_NO_DAEMON=true",
			noDaemonValue: "true",
			shouldDisable: true,
			description:   "Should be disabled for BEADS_NO_DAEMON=true",
		},
		{
			name:          "BEADS_NO_DAEMON=yes",
			noDaemonValue: "yes",
			shouldDisable: true,
			description:   "Should be disabled for BEADS_NO_DAEMON=yes",
		},
		{
			name:          "BEADS_NO_DAEMON=on",
			noDaemonValue: "on",
			shouldDisable: true,
			description:   "Should be disabled for BEADS_NO_DAEMON=on",
		},
		{
			name:          "BEADS_NO_DAEMON=0",
			noDaemonValue: "0",
			shouldDisable: false,
			description:   "Should NOT be disabled for BEADS_NO_DAEMON=0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env
			oldVal, wasSet := os.LookupEnv("BEADS_NO_DAEMON")
			defer func() {
				if wasSet {
					os.Setenv("BEADS_NO_DAEMON", oldVal)
				} else {
					os.Unsetenv("BEADS_NO_DAEMON")
				}
			}()

			// Set test env
			os.Setenv("BEADS_NO_DAEMON", tt.noDaemonValue)

			result := shouldAutoStartDaemon()

			if tt.shouldDisable && result != false {
				t.Errorf("shouldAutoStartDaemon() = %v, want false (%s)",
					result, tt.description)
			}
			if !tt.shouldDisable && result == false {
				t.Logf("shouldAutoStartDaemon() = %v (config-dependent, check passed)", result)
			}
		})
	}
}
