package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestGetPIDFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	oldDBPath := dbPath
	defer func() { dbPath = oldDBPath }()

	dbPath = filepath.Join(tmpDir, ".beads", "test.db")
	pidFile, err := getPIDFilePath()
	if err != nil {
		t.Fatalf("getPIDFilePath failed: %v", err)
	}

	expected := filepath.Join(tmpDir, ".beads", "daemon.pid")
	if pidFile != expected {
		t.Errorf("Expected PID file %s, got %s", expected, pidFile)
	}
	
	if _, err := os.Stat(filepath.Dir(pidFile)); os.IsNotExist(err) {
		t.Error("Expected beads directory to be created")
	}
}

func TestGetLogFilePath(t *testing.T) {
	tests := []struct {
		name     string
		userPath string
		dbPath   string
		expected string
	}{
		{
			name:     "user specified path",
			userPath: "/var/log/bd.log",
			dbPath:   "/tmp/.beads/test.db",
			expected: "/var/log/bd.log",
		},
		{
			name:     "default with dbPath",
			userPath: "",
			dbPath:   "/tmp/.beads/test.db",
			expected: "/tmp/.beads/daemon.log",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldDBPath := dbPath
			defer func() { dbPath = oldDBPath }()
			dbPath = tt.dbPath

			result, err := getLogFilePath(tt.userPath)
			if err != nil {
				t.Fatalf("getLogFilePath failed: %v", err)
			}
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestIsDaemonRunning_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")

	isRunning, pid := isDaemonRunning(pidFile)
	if isRunning {
		t.Errorf("Expected daemon not running, got running with PID %d", pid)
	}
}

func TestIsDaemonRunning_StalePIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")

	if err := os.WriteFile(pidFile, []byte("99999"), 0644); err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	isRunning, pid := isDaemonRunning(pidFile)
	if isRunning {
		t.Errorf("Expected daemon not running (stale PID), got running with PID %d", pid)
	}
}

func TestIsDaemonRunning_CurrentProcess(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "test.pid")

	currentPID := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(currentPID)), 0644); err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	isRunning, pid := isDaemonRunning(pidFile)
	if !isRunning {
		t.Error("Expected daemon running (current process PID)")
	}
	if pid != currentPID {
		t.Errorf("Expected PID %d, got %d", currentPID, pid)
	}
}

func TestDaemonIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(dbDir, "test.db")
	testStore, err := sqlite.New(testDBPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	oldStore := store
	oldDBPath := dbPath
	defer func() {
		testStore.Close()
		store = oldStore
		dbPath = oldDBPath
	}()
	store = testStore
	dbPath = testDBPath

	ctx := context.Background()
	testIssue := &types.Issue{
		Title:       "Test daemon issue",
		Description: "Test description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := testStore.CreateIssue(ctx, testIssue, "test"); err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	pidFile := filepath.Join(dbDir, "daemon.pid")
	_ = pidFile

	if isRunning, _ := isDaemonRunning(pidFile); isRunning {
		t.Fatal("Daemon should not be running at start of test")
	}

	t.Run("start requires git repo", func(t *testing.T) {
		if isGitRepo() {
			t.Skip("Already in a git repo, skipping this test")
		}
	})

	t.Run("status shows not running", func(t *testing.T) {
		if isRunning, _ := isDaemonRunning(pidFile); isRunning {
			t.Error("Daemon should not be running")
		}
	})
}

func TestDaemonPIDFileManagement(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "daemon.pid")

	testPID := 12345
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(testPID)), 0644); err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}

	readPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("Failed to parse PID: %v", err)
	}

	if readPID != testPID {
		t.Errorf("Expected PID %d, got %d", testPID, readPID)
	}

	if err := os.Remove(pidFile); err != nil {
		t.Fatalf("Failed to remove PID file: %v", err)
	}

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("PID file should be removed")
	}
}

func TestDaemonLogFileCreation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	logF, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer logF.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	msg := "Test log message"
	_, err = logF.WriteString(fmt.Sprintf("[%s] %s\n", timestamp, msg))
	if err != nil {
		t.Fatalf("Failed to write to log file: %v", err)
	}

	logF.Sync()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if !strings.Contains(string(content), msg) {
		t.Errorf("Log file should contain message: %s", msg)
	}
}

func TestDaemonIntervalParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"1h", 1 * time.Hour},
		{"30s", 30 * time.Second},
		{"2m30s", 2*time.Minute + 30*time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			d, err := time.ParseDuration(tt.input)
			if err != nil {
				t.Errorf("Failed to parse duration %s: %v", tt.input, err)
			}
			if d != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, d)
			}
		})
	}
}
