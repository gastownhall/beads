// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"os"
	"sync"
	"time"
)

// FreshnessChecker monitors the database file for external modifications.
// It detects when the database file has been replaced (e.g., by git merge)
// and triggers a reconnection to ensure fresh data is visible.
//
// This addresses the issue where the daemon's long-lived SQLite connection
// becomes stale after external file replacement (not just in-place writes).
type FreshnessChecker struct {
	dbPath       string
	lastInode    uint64    // File inode (changes when file is replaced)
	lastMtime    time.Time // File modification time
	lastSize     int64     // File size
	mu           sync.Mutex
	enabled      bool
	onStale      func() error // Callback to reconnect when staleness detected
}

// NewFreshnessChecker creates a new freshness checker for the given database path.
// The onStale callback is called when file replacement is detected.
func NewFreshnessChecker(dbPath string, onStale func() error) *FreshnessChecker {
	fc := &FreshnessChecker{
		dbPath:  dbPath,
		enabled: true,
		onStale: onStale,
	}

	// Capture initial file state
	fc.captureFileState()

	return fc
}

// captureFileState records the current file's inode, mtime, and size.
func (fc *FreshnessChecker) captureFileState() {
	info, err := os.Stat(fc.dbPath)
	if err != nil {
		return
	}

	fc.lastMtime = info.ModTime()
	fc.lastSize = info.Size()

	// Get inode (Unix only, returns 0 on Windows)
	fc.lastInode = getFileInode(info)
}

// Check examines the database file for changes and triggers reconnection if needed.
// Returns true if the file was replaced and reconnection was triggered.
// This method is safe for concurrent use.
func (fc *FreshnessChecker) Check() bool {
	if !fc.enabled || fc.dbPath == "" || fc.dbPath == ":memory:" {
		return false
	}

	fc.mu.Lock()

	info, err := os.Stat(fc.dbPath)
	if err != nil {
		// File disappeared - might be mid-replace, skip this check
		fc.mu.Unlock()
		return false
	}

	// Check if file was replaced by comparing inode
	currentInode := getFileInode(info)

	// Detect file replacement:
	// 1. Inode changed (file was replaced, most reliable on Unix)
	// 2. Mtime changed (file was modified or replaced)
	// 3. Size changed significantly (backup detection)
	fileReplaced := false

	if currentInode != 0 && fc.lastInode != 0 && currentInode != fc.lastInode {
		// Inode changed - file was definitely replaced
		fileReplaced = true
		debugPrintf("FreshnessChecker: inode changed %d -> %d\n", fc.lastInode, currentInode)
	} else if !info.ModTime().Equal(fc.lastMtime) {
		// Mtime changed - file was modified or replaced
		// This catches cases where inode isn't available (Windows, some filesystems)
		fileReplaced = true
		debugPrintf("FreshnessChecker: mtime changed %v -> %v\n", fc.lastMtime, info.ModTime())
	}

	if fileReplaced {
		// Update tracked state before callback
		fc.lastInode = currentInode
		fc.lastMtime = info.ModTime()
		fc.lastSize = info.Size()

		// Release lock BEFORE calling callback to prevent deadlock
		// (callback may call UpdateState which also needs the lock)
		callback := fc.onStale
		fc.mu.Unlock()

		// Trigger reconnection outside the lock
		if callback != nil {
			debugPrintf("FreshnessChecker: triggering reconnection\n")
			_ = callback()
		}
		return true
	}

	fc.mu.Unlock()
	return false
}

// debugPrintf is a no-op in production but can be enabled for debugging
var debugPrintf = func(format string, args ...interface{}) {
	// Uncomment for debugging:
	// fmt.Printf(format, args...)
}

// DebugState returns the current tracked state for testing/debugging.
func (fc *FreshnessChecker) DebugState() (inode uint64, mtime time.Time, size int64) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.lastInode, fc.lastMtime, fc.lastSize
}

// Enable enables freshness checking.
func (fc *FreshnessChecker) Enable() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.enabled = true
	fc.captureFileState()
}

// Disable disables freshness checking.
func (fc *FreshnessChecker) Disable() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.enabled = false
}

// IsEnabled returns whether freshness checking is enabled.
func (fc *FreshnessChecker) IsEnabled() bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.enabled
}

// UpdateState updates the tracked file state after a successful reconnection.
// Call this after reopening the database to establish a new baseline.
func (fc *FreshnessChecker) UpdateState() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.captureFileState()
}
