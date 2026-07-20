// Package storage — iter.go
//
// Iter[T] is a generic streaming-iterator shape that lets storage callers
// walk an unbounded result set without materializing the whole slice.
// Per-method implementations live in the concrete backend packages.
//
// See be-jaavsb (design) and be-yinl4d (architecture) for the motivation.
package storage

import (
	"context"
)

// Iter is a generic single-pass streaming iterator over a backend result
// set. Iter is NOT safe for concurrent use.
//
// Lifecycle:
//
//  1. Storage.Iter*(ctx, ...) returns an Iter and possibly a setup error.
//     If the setup error is non-nil, the iterator is unusable and Close()
//     need not be called (but is safe to call).
//  2. Call Next(ctx) in a loop. Next returns true while a row is available
//     via Value(); false on end-of-results OR on error.
//  3. After Next returns false, Err() reports the iteration error (or nil
//     if the iterator was exhausted cleanly).
//  4. Always call Close(). Close releases backend resources (DB cursor,
//     RPC session, dedicated pool connection). Close is idempotent and
//     always safe to call. The conventional pattern is `defer it.Close()`
//     immediately after the constructor.
//
// Error handling:
//
//   - Sentinel errors (storage.ErrNotFound, storage.ErrPrefixMismatch,
//     etc.) surface from Err() after Next returns false. errors.Is
//     behaves identically across in-process and RPC transports.
//   - Context cancellation surfaces as ctx.Err() from Err(). Callers
//     SHOULD check ctx.Err() inside the iteration body when they intend
//     to short-circuit on cancellation.
//
// Memory model:
//
//   - Value() may return a pointer that is REUSED on the next Next() call.
//     Callers that retain a value across iterations MUST copy. Local
//     backends today allocate fresh structs per row, but the interface
//     keeps the option open for object-pooling or zero-copy scan paths in
//     the future.
//
// Usage:
//
//	it, err := store.IterIssues(ctx, query, filter)
//	if err != nil { return err }
//	defer it.Close()
//	for it.Next(ctx) {
//	    issue := it.Value()
//	    // ... use issue ...
//	}
//	if err := it.Err(); err != nil { return err }
type Iter[T any] interface {
	Next(ctx context.Context) bool
	Value() *T
	Err() error
	Close() error
}

// SliceIter wraps a []*T as an Iter[T]. Used as a stub for Iter* methods
// whose fully streaming implementation has not landed yet — calling
// `NewSliceIter(store.GetXxx(...))` lets the new method land complete on
// the interface while per-method optimization is deferred to follow-up
// children of be-yinl4d.
type SliceIter[T any] struct {
	items  []*T
	idx    int
	closed bool
}

// NewSliceIter constructs a SliceIter from a slice of pointers. The slice
// is retained, not copied, so callers must not mutate it after handing it
// off.
func NewSliceIter[T any](items []*T) *SliceIter[T] {
	return &SliceIter[T]{items: items, idx: -1}
}

// Next advances the iterator. Returns true while a value is available via
// Value(). Once false, the iterator is exhausted (or Close was called).
func (it *SliceIter[T]) Next(_ context.Context) bool {
	if it.closed {
		return false
	}
	it.idx++
	return it.idx < len(it.items)
}

// Value returns the current element, or nil if Next has not been called
// (or has returned false).
func (it *SliceIter[T]) Value() *T {
	if it.idx < 0 || it.idx >= len(it.items) {
		return nil
	}
	return it.items[it.idx]
}

// Err always returns nil — SliceIter cannot fail.
func (it *SliceIter[T]) Err() error { return nil }

// Close marks the iterator exhausted. Idempotent.
func (it *SliceIter[T]) Close() error {
	it.closed = true
	return nil
}

// ForEach drains an Iter, calling fn for each value. Returns the first
// error from fn or from the iterator. Always closes the iterator.
//
// This is a convenience for tests and simple call sites that don't need
// the lifecycle of an explicit Next/Close loop.
func ForEach[T any](ctx context.Context, it Iter[T], fn func(*T) error) (err error) {
	defer func() {
		if cerr := it.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	for it.Next(ctx) {
		if err := fn(it.Value()); err != nil {
			return err
		}
	}
	return it.Err()
}

// Collect drains an Iter into a slice. Provided as a one-line migration
// helper for tests and callers that today read with a slice-returning
// method. Production code that already streams should not use Collect —
// it defeats the point of the iterator.
func Collect[T any](ctx context.Context, it Iter[T]) ([]*T, error) {
	var out []*T
	err := ForEach(ctx, it, func(v *T) error {
		out = append(out, v)
		return nil
	})
	return out, err
}
