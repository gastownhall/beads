package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FlushManager coordinates auto-flush operations using an event-driven architecture.
// All flush state is owned by a single background goroutine, eliminating race conditions.
//
// Architecture:
//   - Single background goroutine owns isDirty, needsFullExport, debounce timer
//   - Commands send events via channels (markDirty, flushNow, shutdown)
//   - No shared mutable state → no race conditions
//
// Thread-safety: All methods are safe to call from multiple goroutines.
type FlushManager struct {
	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Event channels (buffered to prevent blocking)
	markDirtyCh   chan markDirtyEvent  // Request to mark DB dirty
	timerFiredCh  chan struct{}        // Debounce timer fired
	flushNowCh    chan chan error      // Request immediate flush, returns error
	shutdownCh    chan shutdownRequest // Request shutdown with final flush

	// Background goroutine coordination
	wg sync.WaitGroup

	// Configuration
	enabled          bool          // Auto-flush enabled/disabled
	debounceDuration time.Duration // How long to wait before flushing

	// State tracking
	shutdownOnce sync.Once // Ensures Shutdown() is idempotent
}

// markDirtyEvent signals that the database has been modified
type markDirtyEvent struct {
	fullExport bool // If true, do full export instead of incremental
}

// shutdownRequest requests graceful shutdown with optional final flush
type shutdownRequest struct {
	responseCh chan error // Channel to receive shutdown result
}

// NewFlushManager creates a new flush manager and starts the background goroutine.
//
// Parameters:
//   - enabled: Whether auto-flush is enabled (from --no-auto-flush flag)
//   - debounceDuration: How long to wait after last modification before flushing
//
// Returns a FlushManager that must be stopped via Shutdown() when done.
func NewFlushManager(enabled bool, debounceDuration time.Duration) *FlushManager {
	ctx, cancel := context.WithCancel(context.Background())

	fm := &FlushManager{
		ctx:              ctx,
		cancel:           cancel,
		markDirtyCh:      make(chan markDirtyEvent, 10), // Buffered to prevent blocking
		timerFiredCh:     make(chan struct{}, 1),        // Buffered to prevent timer blocking
		flushNowCh:       make(chan chan error, 1),
		shutdownCh:       make(chan shutdownRequest, 1),
		enabled:          enabled,
		debounceDuration: debounceDuration,
	}

	// Start background goroutine
	fm.wg.Add(1)
	go fm.run()

	return fm
}

// MarkDirty marks the database as dirty and schedules a debounced flush.
// Safe to call from multiple goroutines. Non-blocking.
//
// If called multiple times within debounceDuration, only one flush occurs
// after the last call (debouncing).
func (fm *FlushManager) MarkDirty(fullExport bool) {
	if !fm.enabled {
		return
	}

	select {
	case fm.markDirtyCh <- markDirtyEvent{fullExport: fullExport}:
		// Event sent successfully
	case <-fm.ctx.Done():
		// Manager is shutting down, ignore
	}
}

// FlushNow triggers an immediate flush, bypassing debouncing.
// Blocks until flush completes. Returns any error from the flush operation.
//
// Safe to call from multiple goroutines.
func (fm *FlushManager) FlushNow() error {
	if !fm.enabled {
		return nil
	}

	responseCh := make(chan error, 1)

	select {
	case fm.flushNowCh <- responseCh:
		// Wait for response
		return <-responseCh
	case <-fm.ctx.Done():
		return fmt.Errorf("flush manager shut down")
	}
}

// Shutdown gracefully shuts down the flush manager.
// Performs a final flush if the database is dirty, then stops the background goroutine.
// Blocks until shutdown is complete.
//
// Safe to call from multiple goroutines (only first call does work).
// Subsequent calls return nil immediately (idempotent).
func (fm *FlushManager) Shutdown() error {
	var shutdownErr error

	fm.shutdownOnce.Do(func() {
		// Send shutdown request FIRST (before cancelling context)
		// This ensures the run() loop processes the shutdown request
		responseCh := make(chan error, 1)
		select {
		case fm.shutdownCh <- shutdownRequest{responseCh: responseCh}:
			// Wait for shutdown to complete
			err := <-responseCh
			fm.wg.Wait() // Ensure goroutine has exited

			// Cancel context after shutdown completes
			fm.cancel()
			shutdownErr = err
		case <-time.After(30 * time.Second):
			// Timeout waiting for shutdown
			// 30s is generous - most flushes complete in <1s
			// Large databases with thousands of issues may take longer
			// If this timeout fires, we risk losing unflushed data
			fm.cancel()
			shutdownErr = fmt.Errorf("shutdown timeout after 30s - final flush may not have completed")
		}
	})

	return shutdownErr
}

// run is the main event loop, running in a background goroutine.
// Owns all flush state (isDirty, needsFullExport, timer).
// Processes events from channels until shutdown.
func (fm *FlushManager) run() {
	defer fm.wg.Done()

	// State owned by this goroutine only (no mutex needed!)
	var (
		isDirty         = false
		needsFullExport = false
		debounceTimer   *time.Timer
	)

	// Cleanup on exit
	defer func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
	}()

	for {
		select {
		case event := <-fm.markDirtyCh:
			// Mark dirty and schedule debounced flush
			isDirty = true
			if event.fullExport {
				needsFullExport = true
			}

			// Reset debounce timer
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(fm.debounceDuration, func() {
				// Timer fired - notify the run loop to flush
				// Use non-blocking send since channel is buffered
				select {
				case fm.timerFiredCh <- struct{}{}:
					// Notification sent
				default:
					// Channel full (timer fired again before previous flush completed)
					// This is OK - the pending flush will handle it
				}
			})

		case <-fm.timerFiredCh:
			// Debounce timer fired - flush if dirty
			if isDirty {
				_ = fm.performFlush(needsFullExport)
				// Clear dirty flags after successful flush
				isDirty = false
				needsFullExport = false
			}

		case responseCh := <-fm.flushNowCh:
			// Immediate flush requested
			if debounceTimer != nil {
				debounceTimer.Stop()
				debounceTimer = nil
			}

			if !isDirty {
				// Nothing to flush
				responseCh <- nil
				continue
			}

			// Perform the flush
			err := fm.performFlush(needsFullExport)
			if err == nil {
				// Success - clear dirty flags
				isDirty = false
				needsFullExport = false
			}
			responseCh <- err

		case req := <-fm.shutdownCh:
			// Shutdown requested
			if debounceTimer != nil {
				debounceTimer.Stop()
			}

			// Perform final flush if dirty
			var err error
			if isDirty {
				err = fm.performFlush(needsFullExport)
			}

			req.responseCh <- err
			return // Exit goroutine

		case <-fm.ctx.Done():
			// Context cancelled (shouldn't normally happen)
			return
		}
	}
}

// performFlush executes the actual flush operation.
// Called only from the run() goroutine, so no concurrency issues.
func (fm *FlushManager) performFlush(fullExport bool) error {
	// Check if store is still active
	storeMutex.Lock()
	if !storeActive {
		storeMutex.Unlock()
		return nil // Store closed, nothing to do
	}
	storeMutex.Unlock()

	// Call the actual flush implementation with explicit state
	// This avoids race conditions with global isDirty/needsFullExport flags
	flushToJSONLWithState(flushState{
		forceDirty:      true, // We know we're dirty (we wouldn't be here otherwise)
		forceFullExport: fullExport,
	})

	return nil
}
