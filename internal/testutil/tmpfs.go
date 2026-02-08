package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TempDirInMemory creates a temporary directory that preferentially uses
// in-memory filesystems (tmpfs/ramdisk) when available. This reduces I/O
// overhead for git-heavy tests.
//
// On Linux: Uses /dev/shm if available (tmpfs ramdisk)
// On macOS: Falls back to standard temp (ramdisks require manual setup)
// On Windows: Falls back to standard temp
//
// The directory is automatically cleaned up when the test ends.
func TempDirInMemory(t testing.TB) string {
	t.Helper()

	var baseDir string

	switch runtime.GOOS {
	case "linux":
		// Try /dev/shm (tmpfs ramdisk) first
		if stat, err := os.Stat("/dev/shm"); err == nil && stat.IsDir() {
			// Create subdirectory with proper permissions
			tmpBase := filepath.Join("/dev/shm", "beads-test")
			if err := os.MkdirAll(tmpBase, 0755); err == nil {
				baseDir = tmpBase
			}
		}
	case "darwin":
		// macOS: /tmp might already be on APFS with fast I/O
		// Creating a ramdisk requires sudo, so we rely on system defaults
		// Users can manually mount /tmp as tmpfs if needed:
		// diskutil erasevolume HFS+ "ramdisk" `hdiutil attach -nomount ram://2048000`
		baseDir = os.TempDir()
	default:
		// Windows and others: use standard temp
		baseDir = os.TempDir()
	}

	// If we didn't set baseDir (e.g., /dev/shm unavailable), use default
	if baseDir == "" {
		baseDir = os.TempDir()
	}

	// Create unique temp directory
	tmpDir, err := os.MkdirTemp(baseDir, "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Register cleanup
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	return tmpDir
}
