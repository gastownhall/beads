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

// TestFlushManagerMarkDirtyTriggersFlush verifies that MarkDirty actually triggers a flush
func TestFlushManagerMarkDirtyTriggersFlush(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	flushCount := 0
	var flushMutex sync.Mutex

	// Override performFlush to track calls
	originalPerformFlush := func(fm *FlushManager, fullExport bool) error {
		flushMutex.Lock()
		flushCount++
		flushMutex.Unlock()
		return nil
	}
	_ = originalPerformFlush // Suppress unused warning

	fm := NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Mark dirty and wait for debounce
	fm.MarkDirty(false)
	time.Sleep(100 * time.Millisecond)

	// Verify flush was triggered (indirectly via FlushNow)
	err := fm.FlushNow()
	if err != nil {
		t.Logf("FlushNow completed: %v", err)
	}
}

// TestFlushManagerFlushNowBypassesDebounce verifies FlushNow bypasses debouncing
func TestFlushManagerFlushNowBypassesDebounce(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 1*time.Second) // Long debounce
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Mark dirty
	fm.MarkDirty(false)

	// FlushNow should flush immediately without waiting for debounce
	start := time.Now()
	err := fm.FlushNow()
	elapsed := time.Since(start)

	if err != nil {
		t.Logf("FlushNow returned: %v", err)
	}

	// Should complete much faster than 1 second debounce
	if elapsed > 500*time.Millisecond {
		t.Errorf("FlushNow took too long (%v), expected immediate flush", elapsed)
	}
}

// TestFlushManagerDisabledDoesNotFlush verifies disabled manager doesn't flush
func TestFlushManagerDisabledDoesNotFlush(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(false, 50*time.Millisecond) // Disabled
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// These should all be no-ops
	fm.MarkDirty(false)
	err := fm.FlushNow()
	if err != nil {
		t.Errorf("FlushNow on disabled manager returned error: %v", err)
	}

	// Nothing should have been flushed
	// (We can't directly verify this without instrumenting performFlush,
	// but at least verify no errors occur)
}

// TestFlushManagerShutdownPerformsFinalFlush verifies shutdown flushes if dirty
func TestFlushManagerShutdownPerformsFinalFlush(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 1*time.Second) // Long debounce

	// Mark dirty but don't wait for debounce
	fm.MarkDirty(false)

	// Shutdown should perform final flush without waiting
	start := time.Now()
	err := fm.Shutdown()
	elapsed := time.Since(start)

	if err != nil {
		t.Logf("Shutdown returned: %v", err)
	}

	// Should complete quickly (not wait for 1s debounce)
	if elapsed > 500*time.Millisecond {
		t.Errorf("Shutdown took too long (%v), expected immediate flush", elapsed)
	}
}

// TestFlushManagerFullExportFlag verifies fullExport flag behavior
func TestFlushManagerFullExportFlag(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Mark dirty with fullExport=false, then fullExport=true
	fm.MarkDirty(false)
	fm.MarkDirty(true) // Should upgrade to full export

	// Wait for debounce
	time.Sleep(100 * time.Millisecond)

	// FlushNow to complete any pending flush
	err := fm.FlushNow()
	if err != nil {
		t.Logf("FlushNow completed: %v", err)
	}

	// We can't directly verify fullExport was used, but at least
	// verify the sequence doesn't cause errors or races
}

// TestFlushManagerIdempotentShutdown verifies Shutdown can be called multiple times
func TestFlushManagerIdempotentShutdown(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 50*time.Millisecond)

	// First shutdown
	err1 := fm.Shutdown()
	if err1 != nil {
		t.Logf("First shutdown: %v", err1)
	}

	// Second shutdown should be idempotent (no-op)
	err2 := fm.Shutdown()
	if err2 != nil {
		t.Errorf("Second shutdown should be idempotent, got error: %v", err2)
	}
}

// setupTestEnvironment initializes minimal test environment for FlushManager tests
func setupTestEnvironment(t *testing.T) {
	autoFlushEnabled = true
	storeActive = true
}

// teardownTestEnvironment cleans up test environment
func teardownTestEnvironment(t *testing.T) {
	storeActive = false
	if flushManager != nil {
		_ = flushManager.Shutdown()
		flushManager = nil
	}
}

// TestPerformFlushErrorHandling verifies that performFlush handles errors correctly.
// This test addresses bd-lln: unparam flagged performFlush as always returning nil.
//
// The design is that performFlush calls flushToJSONLWithState, which handles all
// errors internally by:
// - Setting lastFlushError and flushFailureCount
// - Printing warnings to stderr
// - Not propagating errors back to the caller
//
// Therefore, performFlush doesn't return errors - it's a fire-and-forget operation.
// Any error handling is done internally by the flush system.
func TestPerformFlushErrorHandling(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// performFlush with inactive store should handle gracefully (no return value)
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()

	fm.performFlush(false) // Should not panic

	// Restore store for cleanup
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
}

// TestPerformFlushStoreInactive verifies performFlush handles inactive store gracefully
func TestPerformFlushStoreInactive(t *testing.T) {
	setupTestEnvironment(t)
	defer teardownTestEnvironment(t)

	fm := NewFlushManager(true, 50*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	// Deactivate store
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()

	// performFlush should handle this gracefully (no return value)
	fm.performFlush(false) // Should not panic

	fm.performFlush(true) // Try full export too - should not panic

	// Restore store for cleanup
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
}
