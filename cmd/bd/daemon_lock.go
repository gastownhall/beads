package main

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
	PID        int       `json:"pid"`
	ParentPID  int       `json:"parent_pid,omitempty"` // Parent process ID (0 if not tracked)
	Database   string    `json:"database"`
	Version    string    `json:"version"`
	StartedAt  time.Time `json:"started_at"`
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
	// Closing the file descriptor automatically releases the flock
	err := l.file.Close()
	l.file = nil
	return err
}

// acquireDaemonLock attempts to acquire an exclusive lock on daemon.lock
// Returns ErrDaemonLocked if another daemon is already running
// dbPath is the full path to the database file (e.g., /path/to/.beads/beads.db)
func acquireDaemonLock(beadsDir string, dbPath string) (*DaemonLock, error) {
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
		ParentPID: os.Getppid(),
		Database:  dbPath,
		Version:   Version,
		StartedAt: time.Now().UTC(),
	}
	
	_ = f.Truncate(0)              // Clear file for fresh write (we hold lock)
	_, _ = f.Seek(0, 0)
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(lockInfo)   // Write can't fail if Truncate succeeded
	_ = f.Sync()                   // Best-effort sync to disk

	// Also write PID file for Windows compatibility (can't read locked files on Windows)
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600) // Best-effort PID write

	return &DaemonLock{file: f, path: lockPath}, nil
}

// tryDaemonLock attempts to acquire and immediately release the daemon lock
// to check if a daemon is running. Returns true if daemon is running.
// Falls back to PID file check for backward compatibility with pre-lock daemons.
func tryDaemonLock(beadsDir string) (running bool, pid int) {
	lockPath := filepath.Join(beadsDir, "daemon.lock")

	// Open lock file with read-write access (required for LockFileEx on Windows)
	// #nosec G304 - controlled path from config
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		// No lock file - could be old daemon without lock support
		// Fall back to PID file check for backward compatibility
		return checkPIDFile(beadsDir)
	}
	defer func() { _ = f.Close() }()

	// Try to acquire lock non-blocking
	if err := flockExclusive(f); err != nil {
		if err == ErrDaemonLocked {
			// Lock is held - daemon is running
			// Try to read PID from JSON format (best effort)
			_, _ = f.Seek(0, 0)
			var lockInfo DaemonLockInfo
			if err := json.NewDecoder(f).Decode(&lockInfo); err == nil {
				pid = lockInfo.PID
			} else {
				// Fallback: try reading as plain integer (old format)
				_, _ = f.Seek(0, 0)
				data := make([]byte, 32)
				n, _ := f.Read(data)
				if n > 0 {
					_, _ = fmt.Sscanf(string(data[:n]), "%d", &pid)
				}
				// Fallback to PID file if we couldn't read PID from lock file
				if pid == 0 {
					_, pid = checkPIDFile(beadsDir)
				}
			}
			return true, pid
		}
		// Other errors mean we can't determine status
		return false, 0
	}

	// We got the lock - no daemon running
	// Release immediately (file close will do this)
	return false, 0
}

// readDaemonLockInfo reads and parses the daemon lock file
// Returns lock info if available, or error if file doesn't exist or can't be parsed
func readDaemonLockInfo(beadsDir string) (*DaemonLockInfo, error) {
	lockPath := filepath.Join(beadsDir, "daemon.lock")
	
	// #nosec G304 - controlled path from config
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, err
	}
	
	var lockInfo DaemonLockInfo
	if err := json.Unmarshal(data, &lockInfo); err != nil {
		// Try parsing as old format (plain PID)
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil {
			return &DaemonLockInfo{PID: pid}, nil
		}
		return nil, fmt.Errorf("cannot parse lock file: %w", err)
	}
	
	return &lockInfo, nil
}

// validateDaemonLock validates that the running daemon matches expected parameters
// Returns error if validation fails (mismatch detected)
func validateDaemonLock(beadsDir string, expectedDB string) error {
	lockInfo, err := readDaemonLockInfo(beadsDir)
	if err != nil {
		// No lock file or can't read - not an error for validation
		return nil
	}
	
	// Validate database path if specified in lock
	if lockInfo.Database != "" && expectedDB != "" {
		if lockInfo.Database != expectedDB {
			return fmt.Errorf("daemon database mismatch: daemon uses %s but expected %s", lockInfo.Database, expectedDB)
		}
	}
	
	// Version mismatch is a warning, not a hard error (handled elsewhere)
	// But we return the info for caller to decide
	if lockInfo.Version != "" && lockInfo.Version != Version {
		// Not a hard error - version compatibility check happens via RPC
		// This is just informational
	}
	
	return nil
}

// checkPIDFile checks if a daemon is running by reading the PID file.
// This is used for backward compatibility with pre-lock daemons.
func checkPIDFile(beadsDir string) (running bool, pid int) {
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	// #nosec G304 - controlled path from config
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false, 0
	}

	pidVal, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}

	if !isProcessRunning(pidVal) {
		return false, 0
	}

	return true, pidVal
}
