package doctor

import (
	"context"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// SharedStore holds a single DoltStore open for the duration of a doctor run,
// preventing the infinite Dolt server restart loop that occurs when each check
// opens and closes its own store (GH#2636).
//
// Usage:
//
//	ss := NewSharedStore(path)
//	defer ss.Close()
//	store := ss.Store() // may be nil if DB doesn't exist or can't open
//
// Check functions that accept a *dolt.DoltStore parameter should use the
// shared store when available, falling back to opening their own store when
// called standalone (e.g., from tests or one-off checks).
type SharedStore struct {
	store    *dolt.DoltStore
	beadsDir string
}

// NewSharedStore opens a single read-only DoltStore for the given repo path.
// If the database doesn't exist or can't be opened, Store() will return nil.
// The caller MUST call Close() when done (typically via defer).
func NewSharedStore(path string) *SharedStore {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	ss := &SharedStore{beadsDir: beadsDir}

	doltPath := getDatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return ss // No database, store stays nil
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return ss // Can't open, store stays nil
	}

	ss.store = store
	return ss
}

// Store returns the shared DoltStore, or nil if the database couldn't be opened.
func (ss *SharedStore) Store() *dolt.DoltStore {
	if ss == nil {
		return nil
	}
	return ss.store
}

// BeadsDir returns the resolved .beads directory path.
func (ss *SharedStore) BeadsDir() string {
	if ss == nil {
		return ""
	}
	return ss.beadsDir
}

// Close closes the underlying DoltStore. Safe to call multiple times.
func (ss *SharedStore) Close() {
	if ss == nil || ss.store == nil {
		return
	}
	_ = ss.store.Close()
	ss.store = nil
}
