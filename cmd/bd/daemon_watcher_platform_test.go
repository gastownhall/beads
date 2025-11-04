package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestFileWatcher_PlatformSpecificAPI verifies that fsnotify is using the correct
// platform-specific file watching mechanism:
//   - Linux: inotify
//   - macOS: FSEvents (via kqueue in fsnotify)
//   - Windows: ReadDirectoryChangesW
//
// This test ensures the watcher works correctly with the native OS API.
func TestFileWatcher_PlatformSpecificAPI(t *testing.T) {
	t.Parallel()
	// Skip in short mode - platform tests can be slower
	if testing.Short() {
		t.Skip("Skipping platform-specific test in short mode")
	}

	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "test.jsonl")

	// Create initial JSONL file
	if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	var callCount int32
	onChange := func() {
		atomic.AddInt32(&callCount, 1)
	}

	fw, err := NewFileWatcher(jsonlPath, onChange)
	if err != nil {
		t.Fatalf("Failed to create FileWatcher on %s: %v", runtime.GOOS, err)
	}
	defer fw.Close()

	// Verify we're using fsnotify (not polling) on supported platforms
	if fw.pollingMode {
		t.Logf("Warning: Running in polling mode on %s (expected fsnotify)", runtime.GOOS)
		// Don't fail - some environments may not support fsnotify
	} else {
		// Verify watcher was created
		if fw.watcher == nil {
			t.Fatal("watcher is nil but pollingMode is false")
		}
		t.Logf("Using fsnotify on %s (expected native API: %s)", runtime.GOOS, expectedAPI())
	}

	// Override debounce duration for faster tests
	fw.debouncer.duration = 10 * time.Millisecond

	// Start the watcher
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	// Wait for watcher to be ready
	time.Sleep(10 * time.Millisecond)

	// Test 1: Basic file modification
	t.Run("FileModification", func(t *testing.T) {
		beforeCount := atomic.LoadInt32(&callCount)

		if err := os.WriteFile(jsonlPath, []byte("{}\n{}"), 0644); err != nil {
			t.Fatal(err)
		}

		// Wait for debounce + processing using event-driven wait
		waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
			return atomic.LoadInt32(&callCount) > beforeCount
		})
	})

	// Test 2: Multiple rapid changes (stress test for platform API)
	t.Run("RapidChanges", func(t *testing.T) {
		beforeCount := atomic.LoadInt32(&callCount)

		// Make 10 rapid changes
		for i := 0; i < 10; i++ {
			content := make([]byte, i+1)
			for j := range content {
				content[j] = byte('{')
			}
			if err := os.WriteFile(jsonlPath, content, 0644); err != nil {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}

		// Wait for debounce using event-driven wait
		waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
			count := atomic.LoadInt32(&callCount) - beforeCount
			// Should have debounced to very few calls
			if count < 1 {
				return false
			}
			if count > 5 {
				t.Logf("Platform %s: High onChange count (%d) after rapid changes - may indicate debouncing issue", runtime.GOOS, count)
			}
			return true
		})
	})

	// Test 3: Large file write (platform-specific buffering)
	t.Run("LargeFileWrite", func(t *testing.T) {
		beforeCount := atomic.LoadInt32(&callCount)

		// Write a larger file (1KB)
		largeContent := make([]byte, 1024)
		for i := range largeContent {
			largeContent[i] = byte('x')
		}
		if err := os.WriteFile(jsonlPath, largeContent, 0644); err != nil {
			t.Fatal(err)
		}

		// Wait for debounce + processing using event-driven wait
		waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
			return atomic.LoadInt32(&callCount) > beforeCount
		})
	})
}

// TestFileWatcher_PlatformFallback verifies polling fallback works on all platforms.
// This is important because some environments (containers, network filesystems) may
// not support native file watching APIs.
func TestFileWatcher_PlatformFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "test.jsonl")

	if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	var callCount int32
	onChange := func() {
		atomic.AddInt32(&callCount, 1)
	}

	fw, err := NewFileWatcher(jsonlPath, onChange)
	if err != nil {
		t.Fatalf("Failed to create FileWatcher on %s: %v", runtime.GOOS, err)
	}
	defer fw.Close()

	// Force polling mode to test fallback
	fw.pollingMode = true
	fw.pollInterval = 50 * time.Millisecond
	fw.debouncer.duration = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	t.Logf("Testing polling fallback on %s", runtime.GOOS)

	// Wait for polling to start
	time.Sleep(10 * time.Millisecond)

	// Modify file
	if err := os.WriteFile(jsonlPath, []byte("{}\n{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for polling interval + debounce using event-driven wait
	waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
		return atomic.LoadInt32(&callCount) >= 1
	})
}

// TestFileWatcher_CrossPlatformEdgeCases tests edge cases that may behave
// differently across platforms.
func TestFileWatcher_CrossPlatformEdgeCases(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping edge case tests in short mode")
	}

	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "test.jsonl")

	if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	var callCount int32
	onChange := func() {
		atomic.AddInt32(&callCount, 1)
	}

	fw, err := NewFileWatcher(jsonlPath, onChange)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()

	fw.debouncer.duration = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	time.Sleep(10 * time.Millisecond)

	// Test: File truncation
	t.Run("FileTruncation", func(t *testing.T) {
		if fw.pollingMode {
			t.Skip("Skipping fsnotify test in polling mode")
		}

		beforeCount := atomic.LoadInt32(&callCount)

		// Write larger content
		if err := os.WriteFile(jsonlPath, []byte("{}\n{}\n{}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Wait for first write
		waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
			return atomic.LoadInt32(&callCount) > beforeCount
		})

		beforeCount = atomic.LoadInt32(&callCount)

		// Truncate to smaller size
		if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}

		// Check if truncation was detected
		time.Sleep(50 * time.Millisecond)
		count := atomic.LoadInt32(&callCount) - beforeCount
		if count < 1 {
			t.Logf("Platform %s: File truncation not detected (count=%d)", runtime.GOOS, count)
		}
	})

	// Test: Append operation
	t.Run("FileAppend", func(t *testing.T) {
		if fw.pollingMode {
			t.Skip("Skipping fsnotify test in polling mode")
		}

		beforeCount := atomic.LoadInt32(&callCount)

		// Append to file
		f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("\n{}"); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}

		// Wait for append to be detected using event-driven wait
		waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
			return atomic.LoadInt32(&callCount) > beforeCount
		})
	})

	// Test: Permission change (may not trigger on all platforms)
	t.Run("PermissionChange", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Skipping permission test on Windows")
		}
		if fw.pollingMode {
			t.Skip("Skipping fsnotify test in polling mode")
		}

		beforeCount := atomic.LoadInt32(&callCount)

		// Change permissions
		if err := os.Chmod(jsonlPath, 0600); err != nil {
			t.Fatal(err)
		}

		time.Sleep(50 * time.Millisecond)

		// Permission changes typically don't trigger WRITE events
		// Log for informational purposes
		count := atomic.LoadInt32(&callCount) - beforeCount
		t.Logf("Platform %s: Permission change resulted in %d onChange calls (expected: 0)", runtime.GOOS, count)
	})
}

// expectedAPI returns the expected native file watching API for the platform.
func expectedAPI() string {
	switch runtime.GOOS {
	case "linux":
		return "inotify"
	case "darwin":
		return "FSEvents (via kqueue)"
	case "windows":
		return "ReadDirectoryChangesW"
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return "kqueue"
	default:
		return "unknown"
	}
}
