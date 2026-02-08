package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// FlushManager Race Condition Tests
// =============================================================================
//
// These tests verify correct FlushManager behavior under concurrent access.
// Run with: go test -race -run TestFlushManager -v
//
// Race conditions being tested:
// 1. Shutdown timeout with large database (simulated)
// 2. Concurrent MarkDirty calls
// 3. Flush during import
// 4. Debounce timer race conditions
// 5. Context cancellation during flush
//
// =============================================================================

// TestFlushManagerShutdownTimeoutLargeDB simulates shutdown timeout with large DB.
//
// Race condition tested: Shutdown initiated while a long-running flush is in progress.
// Shutdown should timeout gracefully without blocking indefinitely.
func TestFlushManagerShutdownTimeoutLargeDB(t *testing.T) {
	// set up test environment
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	// create flush manager with very short debounce
	fm := NewFlushManager(true, 10*time.Millisecond)

	// mark dirty to trigger flush
	fm.MarkDirty(true)

	// wait for debounce
	time.Sleep(50 * time.Millisecond)

	// shutdown should complete within timeout
	start := time.Now()
	err := fm.Shutdown()
	elapsed := time.Since(start)

	if err != nil {
		t.Logf("Shutdown returned error (may be expected): %v", err)
	}

	// should not take longer than shutdown timeout
	if elapsed > 35*time.Second {
		t.Errorf("Shutdown took too long: %v (expected < 35s)", elapsed)
	}

	t.Logf("Shutdown completed in %v", elapsed)
}

// TestFlushManagerConcurrentMarkDirtyRace tests concurrent MarkDirty under race detector.
//
// Race condition tested: Multiple goroutines calling MarkDirty simultaneously.
// Channel send should not cause data races.
func TestFlushManagerConcurrentMarkDirtyRace(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(true, 100*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const numGoroutines = 100
	const callsPerGoroutine = 100

	var wg sync.WaitGroup
	var markCount int32

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			fullExport := (id%2 == 0)
			for j := 0; j < callsPerGoroutine; j++ {
				fm.MarkDirty(fullExport)
				atomic.AddInt32(&markCount, 1)

				// vary timing to increase interleaving
				if j%10 == 0 {
					time.Sleep(time.Microsecond)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Completed %d MarkDirty calls without race", markCount)
}

// TestFlushManagerFlushDuringImport simulates flush occurring during import.
//
// Race condition tested: MarkDirty called while storeActive is being toggled.
// Should handle gracefully without data races.
func TestFlushManagerFlushDuringImport(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(true, 10*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const iterations = 50
	var wg sync.WaitGroup

	// goroutine 1: toggle storeActive (simulating import)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			storeMutex.Lock()
			storeActive = false
			storeMutex.Unlock()

			time.Sleep(time.Millisecond)

			storeMutex.Lock()
			storeActive = true
			storeMutex.Unlock()

			time.Sleep(time.Millisecond)
		}
	}()

	// goroutine 2: mark dirty continuously
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations*10; i++ {
			fm.MarkDirty(i%5 == 0)
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// goroutine 3: request flushes
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = fm.FlushNow()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()

	// verify storeActive is in consistent state
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
}

// TestFlushManagerDebounceTimerRace tests debounce timer race conditions.
//
// Race condition tested: Timer firing while MarkDirty is resetting it.
// Timer operations should not cause data races.
func TestFlushManagerDebounceTimerRace(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	// very short debounce to increase timer race likelihood
	fm := NewFlushManager(true, 1*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const iterations = 1000
	var wg sync.WaitGroup

	// rapidly mark dirty to cause timer resets
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations/10; j++ {
				fm.MarkDirty(false)
				// no sleep - maximum contention on timer
			}
		}()
	}

	wg.Wait()

	// wait for any pending timers
	time.Sleep(50 * time.Millisecond)
}

// TestFlushManagerContextCancellationDuringFlush tests context cancellation.
//
// Race condition tested: FlushManager context cancelled while flush in progress.
// Should clean up without blocking or panicking.
func TestFlushManagerContextCancellationDuringFlush(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(true, 10*time.Millisecond)

	// mark dirty
	fm.MarkDirty(true)

	// start flush in goroutine
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = fm.FlushNow()
	}()

	// cancel context immediately (simulated by shutdown)
	time.Sleep(time.Millisecond)
	if err := fm.Shutdown(); err != nil {
		t.Logf("Shutdown error (may be expected): %v", err)
	}

	// wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(5 * time.Second):
		t.Error("FlushNow did not complete after context cancellation")
	}
}

// TestFlushManagerMultipleShutdowns tests idempotent shutdown behavior.
//
// Race condition tested: Multiple goroutines calling Shutdown simultaneously.
// Only one should do work, others should return immediately.
func TestFlushManagerMultipleShutdowns(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(true, 50*time.Millisecond)

	// mark dirty
	fm.MarkDirty(false)

	const numShutdowns = 10
	var (
		wg           sync.WaitGroup
		successCount int32
		errorCount   int32
	)

	for i := 0; i < numShutdowns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fm.Shutdown()
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else {
				atomic.AddInt32(&errorCount, 1)
			}
		}()
	}

	wg.Wait()

	t.Logf("Multiple shutdowns: %d success, %d errors", successCount, errorCount)

	// all should succeed (idempotent)
	if successCount != numShutdowns {
		t.Errorf("Expected all %d shutdowns to succeed, got %d", numShutdowns, successCount)
	}
}

// TestFlushManagerFlushNowRace tests concurrent FlushNow calls.
//
// Race condition tested: Multiple goroutines calling FlushNow simultaneously.
// Should serialize correctly without data races.
func TestFlushManagerFlushNowRace(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(true, 100*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const numGoroutines = 20
	var (
		wg           sync.WaitGroup
		successCount int32
		errorCount   int32
	)

	// first mark dirty
	fm.MarkDirty(false)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fm.FlushNow()
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else {
				atomic.AddInt32(&errorCount, 1)
				t.Logf("FlushNow error: %v", err)
			}
		}()
	}

	wg.Wait()

	t.Logf("Concurrent FlushNow: %d success, %d errors", successCount, errorCount)

	// most should succeed
	if successCount < int32(numGoroutines/2) {
		t.Errorf("Expected at least %d successes, got %d", numGoroutines/2, successCount)
	}
}

// TestFlushManagerChannelBufferOverflow tests behavior when channels fill up.
//
// Race condition tested: markDirtyCh buffer full while sending.
// Should not block indefinitely.
func TestFlushManagerChannelBufferOverflow(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	// create manager with very long debounce (won't process events quickly)
	fm := NewFlushManager(true, 10*time.Second)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const numCalls = 1000

	// rapidly fill the buffer
	start := time.Now()
	for i := 0; i < numCalls; i++ {
		fm.MarkDirty(false)
	}
	elapsed := time.Since(start)

	t.Logf("Made %d MarkDirty calls in %v", numCalls, elapsed)

	// should complete quickly (not block on full buffer)
	if elapsed > time.Second {
		t.Errorf("MarkDirty blocked too long: %v", elapsed)
	}
}

// TestFlushManagerDisabledRace tests disabled manager under concurrent access.
//
// Race condition tested: Disabled manager receiving concurrent calls.
// All should be no-ops without races.
func TestFlushManagerDisabledRace(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(false, 50*time.Millisecond) // disabled
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const numGoroutines = 50
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < 100; j++ {
				fm.MarkDirty(id%2 == 0)
				if j%10 == 0 {
					_ = fm.FlushNow()
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestFlushManagerShutdownDuringMarkDirty tests shutdown during active marking.
//
// Race condition tested: Shutdown called while MarkDirty goroutines are active.
// Should complete without blocking.
func TestFlushManagerShutdownDuringMarkDirty(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(true, 50*time.Millisecond)

	const numGoroutines = 20
	var wg sync.WaitGroup

	// start marking goroutines
	ctx, cancel := context.WithCancel(context.Background())

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					fm.MarkDirty(false)
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	// let marking run for a bit
	time.Sleep(100 * time.Millisecond)

	// shutdown while marking is active
	shutdownDone := make(chan struct{})
	go func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
		close(shutdownDone)
	}()

	// wait for shutdown with timeout
	select {
	case <-shutdownDone:
		// expected
	case <-time.After(35 * time.Second):
		t.Error("Shutdown did not complete in time")
	}

	// stop marking goroutines
	cancel()
	wg.Wait()
}

// TestFlushManagerFullExportFlagRace tests fullExport flag under concurrent access.
//
// Race condition tested: Multiple goroutines setting different fullExport values.
// The flag should be "sticky" - once true, stays true until flush.
func TestFlushManagerFullExportFlagRace(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	fm := NewFlushManager(true, 100*time.Millisecond)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const numGoroutines = 20
	var wg sync.WaitGroup

	// half set fullExport=true, half set fullExport=false
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			fullExport := (id%2 == 0)
			for j := 0; j < 50; j++ {
				fm.MarkDirty(fullExport)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// trigger final flush
	_ = fm.FlushNow()
}

// TestFlushManagerTimerFireRace tests timer firing while being stopped.
//
// Race condition tested: Timer fires while FlushNow is stopping it.
// Should not cause double flush or panic.
func TestFlushManagerTimerFireRace(t *testing.T) {
	setupTestEnvironmentForRace(t)
	defer teardownTestEnvironmentForRace(t)

	// use exact debounce time to maximize race window
	debounce := 10 * time.Millisecond
	fm := NewFlushManager(true, debounce)
	defer func() {
		if err := fm.Shutdown(); err != nil {
			t.Logf("Shutdown error: %v", err)
		}
	}()

	const iterations = 100
	for i := 0; i < iterations; i++ {
		// mark dirty to start timer
		fm.MarkDirty(false)

		// wait almost exactly until timer would fire
		time.Sleep(debounce - time.Millisecond)

		// call FlushNow which stops timer
		_ = fm.FlushNow()
	}
}

// Helper functions for race tests

func setupTestEnvironmentForRace(t *testing.T) {
	t.Helper()
	autoFlushEnabled = true
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
}

func teardownTestEnvironmentForRace(t *testing.T) {
	t.Helper()
	storeMutex.Lock()
	storeActive = false
	storeMutex.Unlock()
	if flushManager != nil {
		_ = flushManager.Shutdown()
		flushManager = nil
	}
}
