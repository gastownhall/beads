// Package uowstore adapts a unit-of-work provider (internal/storage/uow) to
// storage.Storage, the surface the Go SDK hands to embedders.
//
// It exists so beads.OpenBestAvailable can serve a proxied-server workspace
// through the same per-root bd proxy the CLI uses (GH#8323), instead of
// opening a different store under it. Everything here delegates to the
// provider's roles and use cases; the package adds no SQL of its own.
//
// What is supported:
//
//   - all 33 role accessors (roles.go);
//   - RunInTransaction with DoltStore's contract: the callback runs exactly
//     once, an empty message commits without a Dolt commit, and a commit whose
//     outcome is unknown wraps storage.ErrCommitIndeterminate (tx.go);
//   - the flat methods that are one use-case call (flat.go);
//   - the optional interfaces Gas City asserts (optional.go).
//
// Every other storage.Storage method is a generated stub returning
// *storage.ErrUnsupported with Backend "proxied-server" (unsupported_gen.go).
package uowstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// Backend names this adapter in *storage.ErrUnsupported.
const Backend = "proxied-server"

// ErrStoreClosed is returned by every method called after Close.
var ErrStoreClosed = errors.New("store is closed")

// closeTimeout bounds Close. Closing the provider only stops endpoint
// rediscovery and closes its pool, so it does not need long.
const closeTimeout = 10 * time.Second

// Store is a storage.Storage over a uow.UnitOfWorkProvider.
//
// Close closes the provider and nothing else: the proxy is a detached
// per-root process shared with every other opener, and it exits on its own
// idle timeout, exactly as it does after a CLI command.
type Store struct {
	unsupportedStorage

	p uow.UnitOfWorkProvider

	// mu orders Close against unit-of-work setup: a closed provider's pool is
	// gone, so no NewUOW may start after Close begins. It is held only around
	// the closed check and NewUOW, never across caller work.
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

var _ storage.Storage = (*Store)(nil)

// New wraps p. The caller hands over ownership: Store.Close closes p.
func New(p uow.UnitOfWorkProvider) (*Store, error) {
	if p == nil {
		return nil, errors.New("uowstore: nil provider")
	}
	return &Store{p: p}, nil
}

// Unwrap returns the provider beneath the store.
func (s *Store) Unwrap() uow.UnitOfWorkProvider { return s.p }

// Close closes the provider once; later calls return the first result.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		s.closeErr = s.p.Close(ctx)
	})
	return s.closeErr
}

// IsClosed reports whether Close has been called.
func (s *Store) IsClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// provider returns the provider, or ErrStoreClosed after Close.
func (s *Store) provider() (uow.UnitOfWorkProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrStoreClosed
	}
	return s.p, nil
}

// newUOW begins one unit of work, refusing after Close.
func (s *Store) newUOW(ctx context.Context) (uow.UnitOfWork, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrStoreClosed
	}
	return s.p.NewUOW(ctx)
}

// UnderlyingDB returns the provider's connection pool: for a proxied
// workspace, a pool whose connections speak BDPROXY1 to the per-root proxy.
// It returns nil when the provider has no pool to offer or the store is
// closed.
//
// RAW-SQL ESCAPE HATCH. Statements on this pool bypass the unit of work, so
// they fire no hooks, write nothing to the events journal or versioned
// history, and mint no Dolt commit. The caveat is DoltStore.UnderlyingDB's;
// it is offered for parity (Gas City's proxied open observation and Dolt
// tagging), not as a write path.
func (s *Store) UnderlyingDB() *sql.DB {
	// Held across the provider call, so Close cannot release the pool while
	// it is being read.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil
	}
	if src, ok := uow.UnwrapProvider(s.p).(uow.SQLDBSource); ok {
		return src.UnderlyingDB()
	}
	return nil
}

// DB is UnderlyingDB under DoltStore's other name for it. The same raw-SQL
// caveat applies.
func (s *Store) DB() *sql.DB { return s.UnderlyingDB() }

// errUnsupported is the generated shell's refusal.
func errUnsupported(op string) error {
	return &storage.ErrUnsupported{Op: op, Backend: Backend}
}
