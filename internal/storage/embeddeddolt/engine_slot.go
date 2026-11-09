//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// engineIdleAfter is how long the cached engine outlives the operation that
// last used it (#5397). Once the interval passes with no operation queued, an
// owner-independent timer disposes the engine so another process can open the
// same database without waiting for this store's Close. Continuous activity
// may retain the engine indefinitely: the interval bounds IDLE time only, not
// fairness between waiters.
const engineIdleAfter = 200 * time.Millisecond

// engineOpener opens one embedded engine handle. It is a field so tests can
// count and substitute opens; production always uses OpenSQL.
type engineOpener func(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error)

// engineSlot owns the store's cached embedded engine (#5397). Every operation
// acquires the slot before touching the engine and holds it until the
// operation's transaction (or pinned connection) has ended, which is what
// preserves the no-overlapping-snapshots invariant the per-operation OpenSQL
// open used to provide: one operation at a time sees the engine, so two
// transactions never run against overlapping snapshots in one process.
//
// The engine itself is opened lazily on first use and reused across
// operations, because an engine open is a full cold start (connector, engine,
// session, chunk-journal bootstrap) — the cost #5397 measured at ~110ms even on
// a trivial database. It is disposed on an operation that cannot prove the
// engine state is clean (a failed BEGIN, COMMIT or ROLLBACK, a canceled
// context, a panic, a final Close), and by the idle timer otherwise.
type engineSlot struct {
	// initMu guards lazy construction of sem and the seams' defaults, so a
	// store built without a constructor (tests) is still safe to use.
	initMu sync.Mutex
	sem    chan struct{} // capacity one; the token's presence means "idle"

	// Test seams, installed before first use and never mutated afterwards:
	// opener counts/substitutes engine opens, idleAfter replaces the idle
	// interval (an hour suppresses disposal, milliseconds exercise it).
	opener    engineOpener
	idleAfter time.Duration

	// Everything at or below this line is touched only while holding sem.
	db       *sql.DB
	cleanup  func() error
	gen      uint64 // generation tag invalidating in-flight idle timers
	timer    *time.Timer
	timerErr error // idle-timer cleanup errors, saved for the final Close

	// closed marks a final Close before it waits for the slot, so operations
	// queued behind the closing holder cannot reopen the engine. Atomic
	// because it is written before the semaphore is acquired.
	closed    atomic.Bool
	closeOnce sync.Once
	closeDone chan struct{}
}

// ensureInit builds the slot's semaphore and seam defaults exactly once.
// Seams installed before the first operation are preserved.
func (e *engineSlot) ensureInit() {
	e.initMu.Lock()
	defer e.initMu.Unlock()
	if e.sem == nil {
		e.sem = make(chan struct{}, 1)
		e.sem <- struct{}{}
	}
	if e.opener == nil {
		e.opener = OpenSQL
	}
	if e.idleAfter <= 0 {
		e.idleAfter = engineIdleAfter
	}
	if e.closeDone == nil {
		e.closeDone = make(chan struct{})
	}
}

// acquire takes the slot, blocking until the current holder is done or ctx is
// canceled. A canceled waiter returns its context error without opening the
// engine or running any SQL.
func (e *engineSlot) acquire(ctx context.Context) error {
	select {
	case <-e.sem:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release hands the slot back, arming the idle timer when the engine is still
// cached. The caller must hold the slot.
func (e *engineSlot) release() {
	if e.db != nil && !e.closed.Load() {
		e.armIdle()
	}
	e.sem <- struct{}{}
}

// armIdle invalidates any pending timer and arms a new generation-tagged one.
// The caller must hold the slot.
func (e *engineSlot) armIdle() {
	e.stopTimer()
	e.gen++
	gen := e.gen
	e.timer = time.AfterFunc(e.idleAfter, func() {
		e.tryIdle(gen)
	})
}

// stopTimer invalidates the pending idle timer without disposing anything.
// The caller must hold the slot.
func (e *engineSlot) stopTimer() {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// tryIdle is the idle timer's callback: it try-acquires the slot, and only
// disposes when the generation it was armed for still owns the engine. A
// holder that raced the timer rearms on completion, and a stale timer (stopped
// after its callback was already scheduled) finds a newer generation and
// leaves the engine alone.
func (e *engineSlot) tryIdle(gen uint64) {
	select {
	case <-e.sem:
		if e.gen == gen && e.db != nil && !e.closed.Load() {
			// No operation was in flight to surface a cleanup error to, so
			// save it for the final Close rather than dropping it.
			e.timerErr = errors.Join(e.timerErr, e.disposeHeld())
		}
		e.sem <- struct{}{}
	default:
		// A holder is active; it rearms the timer on completion.
	}
}

// disposeHeld closes the cached engine and drops it from the slot, exactly
// once per cached engine. The caller must hold the slot; the returned error is
// the caller's to classify (join, wrap commit-indeterminate, or save).
func (e *engineSlot) disposeHeld() error {
	if e.db == nil {
		return nil
	}
	cleanup := e.cleanup
	e.db, e.cleanup = nil, nil
	return cleanup()
}

// isClosed reports whether a final Close has marked the slot closed.
func (e *engineSlot) isClosed() bool {
	return e.closed.Load()
}

// close is the final Close's engine half: it marks the slot closed so nothing
// queued behind it can reopen, waits for an active operation to finish,
// invalidates the idle timer, and disposes the engine exactly once. Any
// cleanup error — including ones the idle timer saved — is returned here.
func (e *engineSlot) close() error {
	e.ensureInit()
	e.closed.Store(true)
	if err := e.acquire(context.Background()); err != nil {
		return err // unreachable: Background never cancels
	}
	e.stopTimer()
	disErr := e.disposeHeld()
	e.release() // closed, so no timer is armed; a queued waiter sees errClosed
	e.closeOnce.Do(func() { close(e.closeDone) })
	return errors.Join(e.timerErr, disErr)
}

// waitClosed blocks until a final close that already started has finished
// disposing, so a concurrent Close cannot return before the engine is gone.
func (e *engineSlot) waitClosed() {
	e.ensureInit()
	<-e.closeDone
}

// engineHandle is the cached engine loaned to one operation at a time.
type engineHandle struct {
	slot *engineSlot
	db   *sql.DB
}

// dispose closes the engine this operation ran on, when the operation cannot
// prove the engine state is clean (a failed BEGIN/COMMIT/ROLLBACK, a canceled
// context): the next operation then reopens rather than inheriting a suspect
// engine. The caller must be running inside withEngine.
func (h *engineHandle) dispose() error {
	return h.slot.disposeHeld()
}

// withEngine runs fn with exclusive ownership of the store's cached engine,
// opening it lazily on first use and reusing it across operations. The slot is
// held through the entire callback and any disposal that follows it, so
// callbacks see a serialized engine and whole multi-statement withDBConn
// sequences are as protected as transactions — a connection limit alone would
// not do that.
//
// OpenSQL's pool configuration and fresh-session behavior are unchanged: the
// pool still discards a returned connection's session state (the driver
// reports the session invalid on reuse), so an operation never inherits
// another's session-local state, only the engine.
//
// Errors: an acquire failure returns the context error with no SQL run; a
// failed open returns the opener's error with the slot released; a callback's
// error is returned as-is (the callback classifies its own disposal, if any).
// A panic disposes the engine and propagates.
func (s *EmbeddedDoltStore) withEngine(ctx context.Context, fn func(h *engineHandle) error) error {
	e := &s.engine
	e.ensureInit()
	if err := e.acquire(ctx); err != nil {
		return err
	}
	// From here on we hold the slot through the callback and any disposal.
	defer func() {
		if p := recover(); p != nil {
			// The engine may be mid-operation; the next caller must not
			// inherit it. The panicking callback's own defers (rollback,
			// pinned-conn close) have already run.
			_ = e.disposeHeld()
			e.release()
			panic(p)
		}
	}()

	if err := ctx.Err(); err != nil {
		e.release()
		return err
	}
	if e.closed.Load() {
		e.release()
		return errClosed
	}
	e.stopTimer()
	if e.db == nil {
		db, cleanup, err := e.opener(ctx, s.dataDir, s.database, s.branch)
		if err != nil {
			e.release()
			return err
		}
		e.db, e.cleanup = db, cleanup
	}

	err := fn(&engineHandle{slot: e, db: e.db})

	// A callback that disposed the engine itself left nothing cached. For the
	// rest, dispose when the store closed under the operation or the context
	// died mid-flight: neither leaves the engine provably clean.
	if e.db != nil && (e.closed.Load() || ctx.Err() != nil) {
		if disErr := e.disposeHeld(); disErr != nil {
			err = errors.Join(err, disErr)
		}
	}
	e.release()
	return err
}
