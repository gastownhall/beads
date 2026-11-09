//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
)

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
type engineSlot struct {
	sem chan struct{}

	db      *sql.DB
	cleanup func() error

	// open is the engine opener (OpenSQL outside tests).
	open sqlOpener
}

func newEngineSlot() *engineSlot {
	return &engineSlot{sem: make(chan struct{}, 1), open: OpenSQL}
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
	defer func() { <-e.sem }()

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
		// A final Close that found the slot busy left the engine for this
		// operation to dispose.
		if !reusable || ctx.Err() != nil || s.closed.Load() {
			cleanupErr = errors.Join(cleanupErr, e.dispose())
		}
	}()
	reusable, err = fn(e.db)
	return err, nil
}

// closeEngine disposes the cached engine on the store's final Close. Callers
// have already marked the store closed, so operations queued behind it return
// errClosed; when an operation holds the slot, it disposes the engine as it
// releases it.
func (s *EmbeddedDoltStore) closeEngine() error {
	e := s.engine
	select {
	case e.sem <- struct{}{}:
	default:
		return nil
	}
	defer func() { <-e.sem }()
	return e.dispose()
}
