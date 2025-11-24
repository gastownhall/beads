package main

import (
	"os"
	"testing"
)

func TestLogRotation(t *testing.T) {

	// Set small max size for testing (1 MB)
	os.Setenv("BEADS_DAEMON_LOG_MAX_SIZE", "1")
	os.Setenv("BEADS_DAEMON_LOG_MAX_BACKUPS", "2")
	os.Setenv("BEADS_DAEMON_LOG_MAX_AGE", "7")
	os.Setenv("BEADS_DAEMON_LOG_COMPRESS", "false") // disable for easier testing
	defer func() {
		os.Unsetenv("BEADS_DAEMON_LOG_MAX_SIZE")
		os.Unsetenv("BEADS_DAEMON_LOG_MAX_BACKUPS")
		os.Unsetenv("BEADS_DAEMON_LOG_MAX_AGE")
		os.Unsetenv("BEADS_DAEMON_LOG_COMPRESS")
	}()

	// Test env parsing
	maxSize := getEnvInt("BEADS_DAEMON_LOG_MAX_SIZE", 10)
	if maxSize != 1 {
		t.Errorf("Expected max size 1, got %d", maxSize)
	}

	maxBackups := getEnvInt("BEADS_DAEMON_LOG_MAX_BACKUPS", 3)
	if maxBackups != 2 {
		t.Errorf("Expected max backups 2, got %d", maxBackups)
	}

	compress := getEnvBool("BEADS_DAEMON_LOG_COMPRESS", true)
	if compress {
		t.Errorf("Expected compress false, got true")
	}
}

func TestGetEnvInt(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		defaultValue int
		expected     int
	}{
		{"not set", "", 10, 10},
		{"valid int", "42", 10, 42},
		{"invalid int", "invalid", 10, 10},
		{"zero", "0", 10, 0},
		{"negative", "-5", 10, -5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				os.Setenv("TEST_INT", tt.envValue)
				defer os.Unsetenv("TEST_INT")
			} else {
				os.Unsetenv("TEST_INT")
			}

			result := getEnvInt("TEST_INT", tt.defaultValue)
			if result != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestGetEnvBool(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		defaultValue bool
		expected     bool
	}{
		{"not set default true", "", true, true},
		{"not set default false", "", false, false},
		{"true string", "true", false, true},
		{"1 string", "1", false, true},
		{"false string", "false", true, false},
		{"0 string", "0", true, false},
		{"invalid string", "invalid", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				os.Setenv("TEST_BOOL", tt.envValue)
				defer os.Unsetenv("TEST_BOOL")
			} else {
				os.Unsetenv("TEST_BOOL")
			}

			result := getEnvBool("TEST_BOOL", tt.defaultValue)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestLogFileRotationDefaults(t *testing.T) {
	// Test default values when no env vars set
	os.Unsetenv("BEADS_DAEMON_LOG_MAX_SIZE")
	os.Unsetenv("BEADS_DAEMON_LOG_MAX_BACKUPS")
	os.Unsetenv("BEADS_DAEMON_LOG_MAX_AGE")
	os.Unsetenv("BEADS_DAEMON_LOG_COMPRESS")

	maxSize := getEnvInt("BEADS_DAEMON_LOG_MAX_SIZE", 50)
	if maxSize != 50 {
		t.Errorf("Expected default max size 50, got %d", maxSize)
	}

	maxBackups := getEnvInt("BEADS_DAEMON_LOG_MAX_BACKUPS", 7)
	if maxBackups != 7 {
		t.Errorf("Expected default max backups 7, got %d", maxBackups)
	}

	maxAge := getEnvInt("BEADS_DAEMON_LOG_MAX_AGE", 30)
	if maxAge != 30 {
		t.Errorf("Expected default max age 30, got %d", maxAge)
	}

	compress := getEnvBool("BEADS_DAEMON_LOG_COMPRESS", true)
	if !compress {
		t.Errorf("Expected default compress true, got false")
	}
}
