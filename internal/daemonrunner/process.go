package daemonrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrDaemonLocked = errors.New("daemon lock already held by another process")

// DaemonLockInfo represents the metadata stored in the daemon.lock file
type DaemonLockInfo struct {
	PID       int       `json:"pid"`
	Database  string    `json:"database"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

// DaemonLock represents a held lock on the daemon.lock file
type DaemonLock struct {
	file *os.File
	path string
}

// Close releases the daemon lock
func (l *DaemonLock) Close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// getPIDFilePath returns the path to daemon.pid in the given beads directory
func getPIDFilePath(beadsDir string) string {
	return filepath.Join(beadsDir, "daemon.pid")
}

// getSocketPath returns the path to bd.sock in the given beads directory
func getSocketPath(beadsDir string) string {
	return filepath.Join(beadsDir, "bd.sock")
}

// readPIDFile reads the PID from daemon.pid
func readPIDFile(pidFile string) (int, error) {
	// #nosec G304 - controlled path from config
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID in file: %w", err)
	}
	return pid, nil
}

// writePIDFile writes the current process PID to daemon.pid
func writePIDFile(pidFile string) error {
	return os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600)
}

// ensurePIDFileCorrect verifies PID file has correct PID, fixes if wrong
func ensurePIDFileCorrect(pidFile string) error {
	myPID := os.Getpid()
	if pid, err := readPIDFile(pidFile); err == nil && pid == myPID {
		return nil
	}
	return writePIDFile(pidFile)
}

// checkVersionMismatch checks if database version matches daemon version
func checkVersionMismatch(dbVersion, daemonVersion string) (mismatch bool, missing bool) {
	if dbVersion == "" {
		return false, true
	}
	if dbVersion != daemonVersion {
		return true, false
	}
	return false, false
}

// acquireDaemonLock attempts to acquire an exclusive lock on daemon.lock
func acquireDaemonLock(beadsDir string, dbPath string, version string) (*DaemonLock, error) {
	lockPath := filepath.Join(beadsDir, "daemon.lock")

	// Open or create the lock file
	// #nosec G304 - controlled path from config
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot open lock file: %w", err)
	}

	// Try to acquire exclusive non-blocking lock
	if err := flockExclusive(f); err != nil {
		_ = f.Close()
		if err == ErrDaemonLocked {
			return nil, ErrDaemonLocked
		}
		return nil, fmt.Errorf("cannot lock file: %w", err)
	}

	// Write JSON metadata to the lock file
	lockInfo := DaemonLockInfo{
		PID:       os.Getpid(),
		Database:  dbPath,
		Version:   version,
		StartedAt: time.Now().UTC(),
	}

	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(lockInfo)
	_ = f.Sync()

	// Also write PID file for Windows compatibility
	pidFile := getPIDFilePath(beadsDir)
	_ = writePIDFile(pidFile)

	return &DaemonLock{file: f, path: lockPath}, nil
}
