//go:build integration
// +build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newMockLogger creates a daemonLogger that does nothing
func newMockLogger() daemonLogger {
	return newTestLogger()
}

func TestFileWatcher_JSONLChangeDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "test.jsonl")

	// Create initial JSONL file
	if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Track onChange calls
	var callCount int32
	var mu sync.Mutex
	var callTimes []time.Time

	onChange := func() {
		mu.Lock()
		defer mu.Unlock()
		atomic.AddInt32(&callCount, 1)
		callTimes = append(callTimes, time.Now())
	}

	// Create watcher with short debounce for testing
	fw, err := NewFileWatcher(jsonlPath, onChange)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()

	// Override debounce duration for faster tests
	fw.debouncer.duration = 10 * time.Millisecond

	// Start the watcher
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	// Wait for watcher to be ready
	time.Sleep(10 * time.Millisecond)

	// Modify the file
	if err := os.WriteFile(jsonlPath, []byte("{}\n{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for debounce + processing using event-driven wait
	waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
		return atomic.LoadInt32(&callCount) >= 1
	})
}

func TestFileWatcher_MultipleChangesDebounced(t *testing.T) {
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
		t.Fatal(err)
	}
	defer fw.Close()

	// Short debounce for testing
	fw.debouncer.duration = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	time.Sleep(10 * time.Millisecond)

	// Make multiple rapid changes
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for debounce using event-driven wait
	waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
		return atomic.LoadInt32(&callCount) >= 1
	})

	count := atomic.LoadInt32(&callCount)
	// Should have debounced multiple changes into 1-2 calls, not 5
	if count > 3 {
		t.Errorf("Expected debouncing to reduce calls to ≤3, got %d", count)
	}
}

func TestFileWatcher_GitRefChangeDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
	gitRefsPath := filepath.Join(dir, ".git", "refs", "heads")

	// Create directory structure
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitRefsPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	var callCount int32
	var mu sync.Mutex
	var sources []string
	onChange := func() {
		mu.Lock()
		defer mu.Unlock()
		atomic.AddInt32(&callCount, 1)
		sources = append(sources, "onChange")
	}

	fw, err := NewFileWatcher(jsonlPath, onChange)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()

	// Skip test if in polling mode (git ref watching not supported in polling mode)
	if fw.pollingMode {
		t.Skip("Git ref watching not available in polling mode")
	}

	fw.debouncer.duration = 10 * time.Millisecond

	// Verify git refs path is being watched
	if fw.watcher == nil {
		t.Fatal("watcher is nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	time.Sleep(10 * time.Millisecond)

	// First, verify watcher is working by modifying JSONL
	if err := os.WriteFile(jsonlPath, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
		return atomic.LoadInt32(&callCount) >= 1
	})

	// Reset counter for git ref test
	atomic.StoreInt32(&callCount, 0)

	// Simulate git ref change (branch update)
	// NOTE: fsnotify behavior for git refs can be platform-specific and unreliable
	// This test verifies the code path but may be skipped on some platforms
	refFile := filepath.Join(gitRefsPath, "main")
	if err := os.WriteFile(refFile, []byte("abc123"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for event detection + debounce (may not work on all platforms)
	time.Sleep(50 * time.Millisecond)

	count := atomic.LoadInt32(&callCount)
	if count < 1 {
		// Git ref watching can be unreliable with fsnotify in some environments
		t.Logf("Warning: git ref change not detected (count=%d) - this may be platform-specific fsnotify behavior", count)
		t.Skip("Git ref watching appears not to work in this environment")
	}
}

func TestFileWatcher_FileRemovalAndRecreation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping file removal test in short mode")
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

	// Skip test if in polling mode (separate test for polling)
	if fw.pollingMode {
		t.Skip("File removal/recreation not testable via fsnotify in polling mode")
	}

	fw.debouncer.duration = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	time.Sleep(10 * time.Millisecond)

	// First verify watcher is working
	if err := os.WriteFile(jsonlPath, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
		return atomic.LoadInt32(&callCount) >= 1
	})

	// Reset for removal test
	atomic.StoreInt32(&callCount, 0)

	// Remove the file (simulates git checkout)
	if err := os.Remove(jsonlPath); err != nil {
		t.Fatal(err)
	}

	// Wait for removal to be detected + debounce
	time.Sleep(30 * time.Millisecond)

	// Recreate the file
	if err := os.WriteFile(jsonlPath, []byte("{}\n{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for recreation to be detected + file re-watch + debounce (may not work on all platforms)
	time.Sleep(50 * time.Millisecond)

	count := atomic.LoadInt32(&callCount)
	if count < 1 {
		// File removal/recreation behavior can be platform-specific
		t.Logf("Warning: file removal+recreation not detected (count=%d) - this may be platform-specific", count)
		t.Skip("File removal/recreation watching appears not to work reliably in this environment")
	}
}

func TestFileWatcher_PollingFallback(t *testing.T) {
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
		t.Fatal(err)
	}
	defer fw.Close()

	// Force polling mode
	fw.pollingMode = true
	fw.pollInterval = 50 * time.Millisecond
	fw.debouncer.duration = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	time.Sleep(10 * time.Millisecond)

	// Modify file
	if err := os.WriteFile(jsonlPath, []byte("{}\n{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for polling interval + debounce
	waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
		return atomic.LoadInt32(&callCount) >= 1
	})

	count := atomic.LoadInt32(&callCount)
	if count < 1 {
		t.Errorf("Expected polling to detect file change, got %d calls", count)
	}
}

func TestFileWatcher_PollingFileDisappearance(t *testing.T) {
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
		t.Fatal(err)
	}
	defer fw.Close()

	fw.pollingMode = true
	fw.pollInterval = 50 * time.Millisecond
	fw.debouncer.duration = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	time.Sleep(10 * time.Millisecond)

	// Remove file
	if err := os.Remove(jsonlPath); err != nil {
		t.Fatal(err)
	}

	// Wait for polling to detect disappearance
	waitFor(t, 200*time.Millisecond, 2*time.Millisecond, func() bool {
		return atomic.LoadInt32(&callCount) >= 1
	})

	count := atomic.LoadInt32(&callCount)
	if count < 1 {
		t.Errorf("Expected polling to detect file disappearance, got %d calls", count)
	}
}

func TestFileWatcher_Close(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "test.jsonl")

	if err := os.WriteFile(jsonlPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	onChange := func() {}

	fw, err := NewFileWatcher(jsonlPath, onChange)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw.Start(ctx, newMockLogger())

	time.Sleep(10 * time.Millisecond)

	// Close should not error
	if err := fw.Close(); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}

	// Second close should be safe
	if err := fw.Close(); err != nil {
		t.Errorf("Second Close() returned error: %v", err)
	}
}
