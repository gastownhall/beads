package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// SharedStore holds a single Dolt store connection that is shared across all
// doctor checks within a single runDiagnostics invocation. This prevents the
// infinite server restart loop where each check's store.Close() stops the
// auto-started Dolt server, causing the next check's store open to restart it.
//
// See GH#2636: 668 restarts and 60+ zombie processes from doctor checks each
// independently opening and closing their own store.
type SharedStore struct {
	store    *dolt.DoltStore
	beadsDir string
	mu       sync.Mutex
	closed   bool
}

// OpenSharedStore opens a single DoltStore for the given beads directory.
// The caller must call Close() when all checks are complete.
// Returns nil (not an error) if the database path doesn't exist or the
// backend is not Dolt — callers should handle nil gracefully.
func OpenSharedStore(beadsDir string) (*SharedStore, error) {
	if !IsDoltBackend(beadsDir) {
		return nil, nil
	}

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return nil, nil
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithCLIOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to open shared store: %w", err)
	}

	return &SharedStore{
		store:    store,
		beadsDir: beadsDir,
	}, nil
}

// Store returns the underlying DoltStore, or nil if the SharedStore is nil or closed.
func (ss *SharedStore) Store() *dolt.DoltStore {
	if ss == nil {
		return nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed {
		return nil
	}
	return ss.store
}

// DB returns the underlying *sql.DB, or nil if the SharedStore is nil or closed.
func (ss *SharedStore) DB() *sql.DB {
	s := ss.Store()
	if s == nil {
		return nil
	}
	return s.UnderlyingDB()
}

// DoltStorage returns the store as a storage.DoltStorage interface,
// or nil if the SharedStore is nil or closed.
func (ss *SharedStore) DoltStorage() storage.DoltStorage {
	s := ss.Store()
	if s == nil {
		return nil
	}
	return s
}

// Close releases the shared store. Safe to call multiple times.
func (ss *SharedStore) Close() {
	if ss == nil {
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed {
		return
	}
	ss.closed = true
	if ss.store != nil {
		_ = ss.store.Close()
	}
}
