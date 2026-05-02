//go:build cgo

package embeddeddolt

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
)

var (
	cacheMu sync.Mutex
	cache   = make(map[string]*cacheEntry) // keyed by absolute dataDir
)

type cacheEntry struct {
	store    *EmbeddedDoltStore
	refCount int // guarded by cacheMu
}

// Open returns a cached EmbeddedDoltStore for the given data directory, creating
// one via newStore if no cached instance exists. Subsequent calls with the same
// resolved dataDir return the existing store and increment a reference count.
//
// Each Open must be paired with a Close. The underlying store is only truly
// closed when the last reference calls Close.
//
// This prevents redundant engine initializations when multiple code paths open
// connectors against the same data directory in the same process.
func Open(ctx context.Context, beadsDir, database, branch string) (*EmbeddedDoltStore, error) {
	key, err := cacheKey(beadsDir)
	if err != nil {
		return nil, err
	}

	cacheMu.Lock()
	if entry, ok := cache[key]; ok {
		entry.refCount++
		cacheMu.Unlock()
		return entry.store, nil
	}
	cacheMu.Unlock()

	// Slow path: create a new store outside the lock.
	s, err := newStore(ctx, beadsDir, database, branch)
	if err != nil {
		return nil, err
	}

	cacheMu.Lock()
	// Double-check: another goroutine may have inserted while we created.
	if entry, ok := cache[key]; ok {
		cacheMu.Unlock()
		// Discard the store we just created; use the cached one.
		_ = s.Close()
		entry.refCount++
		return entry.store, nil
	}
	cache[key] = &cacheEntry{store: s, refCount: 1}
	cacheMu.Unlock()
	return s, nil
}

// closeCached decrements the reference count for a cached store.
// Returns true when the cache absorbed the close (refs remain, suppress real
// close). Returns false when the caller must run Close — either the
// entry was evicted (last ref) or the store was never cached.
func closeCached(s *EmbeddedDoltStore) bool {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	for key, entry := range cache {
		if entry.store == s {
			entry.refCount--
			if entry.refCount <= 0 {
				delete(cache, key)
				// Actual close happens after releasing cacheMu (via caller).
				return false
			}
			// Other references remain — suppress the real close.
			return true
		}
	}
	// Not in cache — let the caller close normally.
	return false
}

// cacheKey resolves beadsDir to an absolute dataDir path for use as a cache key.
func cacheKey(beadsDir string) (string, error) {
	absBeadsDir, err := filepath.Abs(beadsDir)
	if err != nil {
		return "", fmt.Errorf("embeddeddolt: resolving beads dir for cache key: %w", err)
	}
	return filepath.Join(absBeadsDir, "embeddeddolt"), nil
}
