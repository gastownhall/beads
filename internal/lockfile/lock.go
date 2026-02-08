package lockfile

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

// ErrLocked is returned when a lock cannot be acquired because it is held by another process.
var ErrLocked = errDaemonLocked

// ErrLockBusy is returned when a non-blocking lock cannot be acquired
// because another process holds a conflicting lock.
var ErrLockBusy = errors.New("lock busy: held by another process")

// IsLocked returns true if the error indicates a lock is held by another process.
func IsLocked(err error) bool {
	return err == errDaemonLocked
}

// LockInfo represents the metadata stored in the daemon.lock file
type LockInfo struct {
	PID       int       `json:"pid"`
	ParentPID int       `json:"parent_pid,omitempty"`
	Database  string    `json:"database"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

// TryDaemonLock attempts to acquire and immediately release the daemon lock
// to check if a daemon is running. Returns true if daemon is running.
// Falls back to PID file check for backward compatibility with pre-lock daemons.
//
// This is a cheap probe operation that should be called before attempting
// RPC connections to avoid unnecessary connection timeouts.
func TryDaemonLock(beadsDir string) (running bool, pid int) {
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
		if err == errDaemonLocked {
			// Lock is held - daemon is running
			// Try to read PID from JSON format (best effort)
			_, _ = f.Seek(0, 0)
			var lockInfo LockInfo
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

// ReadLockInfo reads and parses the daemon lock file
// Returns lock info if available, or error if file doesn't exist or can't be parsed
func ReadLockInfo(beadsDir string) (*LockInfo, error) {
	lockPath := filepath.Join(beadsDir, "daemon.lock")

	// #nosec G304 - controlled path from config
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, err
	}

	var lockInfo LockInfo
	if err := json.Unmarshal(data, &lockInfo); err != nil {
		// Try parsing as old format (plain PID)
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil {
			return &LockInfo{PID: pid}, nil
		}
		return nil, fmt.Errorf("cannot parse lock file: %w", err)
	}

	return &lockInfo, nil
}
