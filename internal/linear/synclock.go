package linear

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

const syncLockFilename = ".linear-sync.lock"

// SyncLock serializes concurrent bd linear sync invocations within a workspace.
// It combines a PID-annotated lock file with flock for robust mutual exclusion.
type SyncLock struct {
	path string
	file *os.File
}

// SyncLockInfo contains metadata written into the lock file.
type SyncLockInfo struct {
	PID     int
	Started time.Time
}

// AcquireSyncLock acquires the sync lock for the given beads directory.
// If wait is true, blocks until the lock is available. If false, returns
// an error immediately when the lock is held by another live process.
func AcquireSyncLock(beadsDir string, wait bool) (*SyncLock, error) {
	lockPath := filepath.Join(beadsDir, syncLockFilename)

	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		return nil, fmt.Errorf("creating beads directory: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- lockPath is constrained to the beads directory.
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	if wait {
		if err := lockfile.FlockExclusiveBlocking(f); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("acquiring lock (blocking): %w", err)
		}
	} else {
		if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
			if lockfile.IsLocked(err) || err == lockfile.ErrLockBusy {
				info := readLockInfo(lockPath)
				_ = f.Close()
				return nil, &SyncLockHeldError{Info: info}
			}
			_ = f.Close()
			return nil, fmt.Errorf("acquiring lock (non-blocking): %w", err)
		}
	}

	if err := writeLockInfo(f); err != nil {
		_ = lockfile.FlockUnlock(f)
		_ = f.Close()
		return nil, fmt.Errorf("writing lock info: %w", err)
	}

	return &SyncLock{path: lockPath, file: f}, nil
}

// Release releases the sync lock. The lock file is NOT removed — removing it
// after unlocking creates a race where a blocked waiter acquires the old inode
// while a new process creates a fresh file at the same path, splitting lock
// identity. Instead the file is truncated while still holding the flock, then
// the flock is released. The stable path is reused by subsequent Acquire calls.
func (l *SyncLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}

	// Clear diagnostic metadata while still holding the flock so no reader
	// observes a stale PID after we unlock. Truncate failure is non-fatal
	// (metadata-only), but we capture it to surface if unlock/close succeed.
	var truncErr error
	if err := l.file.Truncate(0); err != nil {
		truncErr = fmt.Errorf("clearing lock metadata: %w", err)
	}

	unlockErr := lockfile.FlockUnlock(l.file)
	closeErr := l.file.Close()
	l.file = nil

	if unlockErr != nil {
		return unlockErr
	}
	if closeErr != nil {
		return closeErr
	}
	return truncErr
}

// SyncLockHeldError is returned when the lock is held by another process.
type SyncLockHeldError struct {
	Info *SyncLockInfo
}

func (e *SyncLockHeldError) Error() string {
	if e.Info != nil {
		return fmt.Sprintf("another bd linear sync is already running (PID %d, started %s)",
			e.Info.PID, e.Info.Started.Format(time.RFC3339))
	}
	return "another bd linear sync is already running"
}

func writeLockInfo(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncating lock file: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	info := fmt.Sprintf("pid=%d\nstarted=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_, err := f.WriteString(info)
	return err
}

func readLockInfo(path string) *SyncLockInfo {
	data, err := os.ReadFile(path) // #nosec G304 -- path is constructed from beadsDir, not user input
	if err != nil {
		return nil
	}
	return parseLockInfo(string(data))
}

func parseLockInfo(content string) *SyncLockInfo {
	info := &SyncLockInfo{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok {
			switch k {
			case "pid":
				if pid, err := strconv.Atoi(v); err == nil {
					info.PID = pid
				}
			case "started":
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					info.Started = t
				}
			}
		}
	}
	if info.PID == 0 {
		return nil
	}
	return info
}

// IsProcessAlive checks whether the given PID corresponds to a running process.
// Uses signal 0 on Unix; on Windows, OpenProcess is used (see platform files).
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return isProcessAlive(pid)
}
