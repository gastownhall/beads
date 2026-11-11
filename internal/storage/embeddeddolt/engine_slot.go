//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// defaultEngineIdle is how long a store keeps its engine open after its last
// operation. It is long enough to carry a command's back-to-back operations on
// one engine; once it passes with no operation running, an owner-independent
// timer disposes the engine so another process can open the same database
// without waiting for this store's Close. Continuous activity may retain the
// engine indefinitely: the expiry bounds idle time only, not fairness between
// waiters.
const defaultEngineIdle = 200 * time.Millisecond

// sqlOpener opens an embedded engine the way OpenSQL does. It is a per-store
// seam so tests can count engine opens; production stores always use OpenSQL.
type sqlOpener func(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error)

// engineSlot owns the one embedded engine a store reuses across operations
// (gastownhall/beads#5397). Before it, every operation opened and destroyed a
// whole engine — connector, engine, chunk-journal bootstrap — so a caller
// looping over N issues paid N engine lifecycles.
//
// sem is a capacity-one semaphore held for an entire operation: the callback
// and its transaction or connection cleanup. It is what serializes this
// store's operations now that they no longer exclude each other by opening
// separate engines, so two of them never run against overlapping snapshots
// (gastownhall/beads#6716). A connection limit alone would not do: a
// withDBConn callback may run several statements over the pool. Every field
// below sem is guarded by holding it.
//
// Each operation still gets fresh driver sessions: dolthub/driver never reuses
// a session (DoltConn.IsValid is false and ResetSession returns ErrBadConn), so
// no session-local state crosses operations. Only the engine is shared.
//
// An engine is not held for the store's lifetime: it is released once the
// store has been idle for idle, so other processes (and other stores) can open
// the database between this store's bursts of work.
type engineSlot struct {
	sem chan struct{}

	db      *sql.DB
	cleanup func() error

	// gen advances on every acquire and every arming of timer, so an idle
	// timer that fires late can tell it no longer speaks for the current
	// idle period.
	gen   uint64
	timer *time.Timer
	// idleErr keeps the errors of idle disposals, which have no caller, for
	// the store's Close to report.
	idleErr error
	// disposed is closed once the final Close has disposed the engine, for
	// concurrent Closes to wait on.
	disposed chan struct{}

	// open is the engine opener (OpenSQL outside tests) and idle the expiry
	// (defaultEngineIdle outside tests).
	open sqlOpener
	idle time.Duration
}

func newEngineSlot() *engineSlot {
	return &engineSlot{sem: make(chan struct{}, 1), disposed: make(chan struct{}), open: OpenSQL, idle: defaultEngineIdle}
}

// dispose closes the cached engine, if any. Callers hold sem.
func (e *engineSlot) dispose() error {
	if e.db == nil {
		return nil
	}
	cleanup := e.cleanup
	e.db, e.cleanup = nil, nil
	return cleanup()
}

// withEngine runs fn against the store's cached engine, opening it lazily,
// while holding the engine slot for fn's whole run. fn reports whether the
// engine is still fit for reuse; it is disposed when fn says it is not
// (a failed begin, commit or rollback), when ctx was canceled, and when fn
// panics (the panic propagates). The returned cleanupErr is that disposal's
// error, kept apart from fn's so callers can classify it (a cleanup failure
// after a SQL commit is indeterminate, not a failed write).
//
// A caller waiting for the slot gives up with ctx's error, running no SQL,
// once ctx is done. Callers check s.closed and the read-only guard first;
// withEngine rechecks closed once it holds the slot, so an operation queued
// behind a final Close never reopens the engine.
func (s *EmbeddedDoltStore) withEngine(ctx context.Context, fn func(db *sql.DB) (reusable bool, err error)) (err, cleanupErr error) {
	e := s.engine
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err(), nil
	}
	e.gen++
	if e.timer != nil {
		e.timer.Stop()
	}
	defer func() {
		if e.db != nil && !s.closed.Load() {
			e.gen++
			gen := e.gen
			e.timer = time.AfterFunc(e.idle, func() { s.expireIdle(gen) })
		}
		<-e.sem
	}()

	if s.closed.Load() {
		return errClosed, nil
	}
	if err := ctx.Err(); err != nil {
		return err, nil
	}
	if e.db == nil {
		db, cleanup, openErr := e.open(ctx, s.dataDir, s.database, s.branch)
		if openErr != nil {
			return openErr, nil
		}
		e.db, e.cleanup = db, cleanup
	}

	reusable := false
	defer func() {
		if !reusable || ctx.Err() != nil {
			cleanupErr = errors.Join(cleanupErr, e.dispose())
		}
	}()
	reusable, err = fn(e.db)
	return err, nil
}

// expireIdle disposes the engine once the idle period identified by gen has
// run out. A busy slot means an operation is running; it rearms the timer on
// release, so expiry just steps aside.
func (s *EmbeddedDoltStore) expireIdle(gen uint64) {
	e := s.engine
	select {
	case e.sem <- struct{}{}:
	default:
		return
	}
	defer func() { <-e.sem }()
	if e.gen == gen {
		e.idleErr = errors.Join(e.idleErr, e.dispose())
	}
}

// closeEngine disposes the cached engine on the store's final Close, after
// waiting for the active operation to finish. Callers have already marked the
// store closed, so operations queued behind it return errClosed instead of
// reopening the engine.
func (s *EmbeddedDoltStore) closeEngine() error {
	e := s.engine
	defer close(e.disposed)
	e.sem <- struct{}{}
	defer func() { <-e.sem }()
	e.gen++
	if e.timer != nil {
		e.timer.Stop()
	}
	return errors.Join(e.idleErr, e.dispose())
}
