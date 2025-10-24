package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrDaemonLocked = errors.New("daemon lock already held by another process")

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
func acquireDaemonLock(beadsDir string, _ bool) (*DaemonLock, error) {
	lockPath := filepath.Join(beadsDir, "daemon.lock")

	// Open or create the lock file
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

	// Write our PID to the lock file for debugging (optional)
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	// Also write PID file for Windows compatibility (can't read locked files on Windows)
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600)

	return &DaemonLock{file: f, path: lockPath}, nil
}

// tryDaemonLock attempts to acquire and immediately release the daemon lock
// to check if a daemon is running. Returns true if daemon is running.
// Falls back to PID file check for backward compatibility with pre-lock daemons.
func tryDaemonLock(beadsDir string) (running bool, pid int) {
	lockPath := filepath.Join(beadsDir, "daemon.lock")

	// Open lock file with read-write access (required for LockFileEx on Windows)
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
			// Try to read PID for display (best effort)
			_, _ = f.Seek(0, 0) // Seek to beginning before reading
			data := make([]byte, 32)
			n, _ := f.Read(data)
			if n > 0 {
				_, _ = fmt.Sscanf(string(data[:n]), "%d", &pid)
			}
			// Fallback to PID file if we couldn't read PID from lock file
			if pid == 0 {
				_, pid = checkPIDFile(beadsDir)
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
