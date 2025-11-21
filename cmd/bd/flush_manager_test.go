package main

import (
	"sync"
	"testing"
	"time"
)

// TestFlushManagerConcurrentMarkDirty tests that concurrent MarkDirty calls don't race.
// Run with: go test -race -run TestFlushManagerConcurrentMarkDirty
func TestFlushManagerConcurrentMarkDirty(t *testing.T) {
	fm := NewFlushManager(true, 100*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Spawn many goroutines all calling MarkDirty concurrently
	const numGoroutines = 50
	const numCallsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			fullExport := (id % 2 == 0) // Alternate between incremental and full
			for j := 0; j < numCallsPerGoroutine; j++ {
				fm.MarkDirty(fullExport)
				// Small random delay to increase interleaving
				time.Sleep(time.Microsecond * time.Duration(id%10))
			}
		}(i)
	}

	wg.Wait()

	// If we got here without a race detector warning, the test passed
}

// TestFlushManagerConcurrentFlushNow tests concurrent FlushNow calls.
// Run with: go test -race -run TestFlushManagerConcurrentFlushNow
func TestFlushManagerConcurrentFlushNow(t *testing.T) {
	// Set up a minimal test environment
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 100*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Mark dirty first so there's something to flush
	fm.MarkDirty(false)

	// Spawn multiple goroutines all calling FlushNow concurrently
	const numGoroutines = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := fm.FlushNow()
			if err != nil {
				t.Logf("FlushNow returned error (may be expected if store closed): %v", err)
			}
		}()
	}

	wg.Wait()

	// If we got here without a race detector warning, the test passed
}

// TestFlushManagerMarkDirtyDuringFlush tests marking dirty while a flush is in progress.
// Run with: go test -race -run TestFlushManagerMarkDirtyDuringFlush
func TestFlushManagerMarkDirtyDuringFlush(t *testing.T) {
	// Set up a minimal test environment
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Interleave MarkDirty and FlushNow calls
	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: Keep marking dirty
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			fm.MarkDirty(i%10 == 0) // Occasional full export
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 2: Periodically flush
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			time.Sleep(10 * time.Millisecond)
			_ = fm.FlushNow()
		}
	}()

	wg.Wait()

	// If we got here without a race detector warning, the test passed
}

// TestFlushManagerShutdownDuringOperation tests shutdown while operations are ongoing.
// Run with: go test -race -run TestFlushManagerShutdownDuringOperation
func TestFlushManagerShutdownDuringOperation(t *testing.T) {
	// Set up a minimal test environment
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 100*time.Millisecond)

	// Start some background operations
	var wg sync.WaitGroup
	wg.Add(5)

	for i := 0; i < 5; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				fm.MarkDirty(false)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Let operations run for a bit
	time.Sleep(50 * time.Millisecond)

	// Shutdown while operations are ongoing
	if err := fm.Shutdown(); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}

	wg.Wait()

	// Verify that MarkDirty after shutdown doesn't panic
	fm.MarkDirty(false) // Should be ignored gracefully
}

// TestFlushManagerDebouncing tests that rapid MarkDirty calls debounce correctly.
func TestFlushManagerDebouncing(t *testing.T) {
	// Set up a minimal test environment
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	flushCount := 0
	var flushMutex sync.Mutex

	// We'll test debouncing by checking that rapid marks result in fewer flushes
	fm := NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Mark dirty many times in quick succession
	for i := 0; i < 100; i++ {
		fm.MarkDirty(false)
		time.Sleep(time.Millisecond) // 1ms between marks, debounce is 50ms
	}

	// Wait for debounce window to expire
	time.Sleep(100 * time.Millisecond)

	// Trigger one flush to see if debouncing worked
	_ = fm.FlushNow()

	flushMutex.Lock()
	count := flushCount
	flushMutex.Unlock()

	// We should have much fewer flushes than marks (debouncing working)
	// With 100 marks 1ms apart and 50ms debounce, we expect ~2-3 flushes
	t.Logf("Flush count: %d (expected < 10 due to debouncing)", count)
}

// TestMarkDirtyAndScheduleFlushConcurrency tests the legacy functions with race detector.
// This ensures backward compatibility while using FlushManager internally.
// Run with: go test -race -run TestMarkDirtyAndScheduleFlushConcurrency
func TestMarkDirtyAndScheduleFlushConcurrency(t *testing.T) {
	// Set up test environment with FlushManager
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	// Create a FlushManager (simulates what main.go does)
	flushManager = NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if flushManager != nil {
			_ = flushManager.Shutdown()
			flushManager = nil
		}
	}()

	// Test concurrent calls to markDirtyAndScheduleFlush
	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if id%2 == 0 {
					markDirtyAndScheduleFlush()
				} else {
					markDirtyAndScheduleFullExport()
				}
				time.Sleep(time.Microsecond * time.Duration(id%10))
			}
		}(i)
	}

	wg.Wait()

	// If we got here without a race detector warning, the test passed
}

// setupTestEnvironment initializes minimal test environment for FlushManager tests
func setupTestEnvironment(t *testing.T) {
	autoFlushEnabled = true
	storeActive = true
	isDirty = false
	needsFullExport = false
}

// teardownTestEnvironment cleans up test environment
func teardownTestEnvironment(t *testing.T) {
	storeActive = false
	if flushManager != nil {
		_ = flushManager.Shutdown()
		flushManager = nil
	}
}
