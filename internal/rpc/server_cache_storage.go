package rpc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// StorageCacheEntry holds a cached storage with metadata for eviction
type StorageCacheEntry struct {
	store      storage.Storage
	lastAccess time.Time
	dbMtime    time.Time // DB file modification time for detecting external changes
}

// runCleanupLoop periodically evicts stale storage connections and checks memory pressure
func (s *Server) runCleanupLoop() {
	s.cleanupTicker = time.NewTicker(5 * time.Minute)
	defer s.cleanupTicker.Stop()

	for {
		select {
		case <-s.cleanupTicker.C:
			s.checkMemoryPressure()
			s.evictStaleStorage()
		case <-s.shutdownChan:
			return
		}
	}
}

// checkMemoryPressure monitors memory usage and triggers aggressive eviction if needed
func (s *Server) checkMemoryPressure() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Memory thresholds (configurable via env var)
	const defaultThresholdMB = 500
	thresholdMB := defaultThresholdMB
	if env := os.Getenv("BEADS_DAEMON_MEMORY_THRESHOLD_MB"); env != "" {
		var threshold int
		if _, err := fmt.Sscanf(env, "%d", &threshold); err == nil && threshold > 0 {
			thresholdMB = threshold
		}
	}

	allocMB := m.Alloc / 1024 / 1024
	// #nosec G115 - safe conversion of positive value
	if allocMB > uint64(thresholdMB) {
		fmt.Fprintf(os.Stderr, "Warning: High memory usage detected (%d MB), triggering aggressive cache eviction\n", allocMB)
		s.aggressiveEviction()
		runtime.GC() // Suggest garbage collection
	}
}

// aggressiveEviction evicts 50% of cached storage to reduce memory pressure
func (s *Server) aggressiveEviction() {
	toClose := []storage.Storage{}

	s.cacheMu.Lock()

	if len(s.storageCache) == 0 {
		s.cacheMu.Unlock()
		return
	}

	// Build sorted list by last access
	type cacheItem struct {
		path  string
		entry *StorageCacheEntry
	}
	items := make([]cacheItem, 0, len(s.storageCache))
	for path, entry := range s.storageCache {
		items = append(items, cacheItem{path, entry})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].entry.lastAccess.Before(items[j].entry.lastAccess)
	})

	// Evict oldest 50%
	numToEvict := len(items) / 2
	for i := 0; i < numToEvict; i++ {
		toClose = append(toClose, items[i].entry.store)
		delete(s.storageCache, items[i].path)
	}

	s.cacheMu.Unlock()

	// Close without holding lock
	for _, store := range toClose {
		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close evicted storage: %v\n", err)
		}
	}
}

// evictStaleStorage removes idle connections and enforces cache size limits
func (s *Server) evictStaleStorage() {
	now := time.Now()
	toClose := []storage.Storage{}

	s.cacheMu.Lock()

	// First pass: evict TTL-expired entries
	for path, entry := range s.storageCache {
		if now.Sub(entry.lastAccess) > s.cacheTTL {
			toClose = append(toClose, entry.store)
			delete(s.storageCache, path)
		}
	}

	// Second pass: enforce max cache size with LRU eviction
	if len(s.storageCache) > s.maxCacheSize {
		// Build sorted list of entries by lastAccess
		type cacheItem struct {
			path  string
			entry *StorageCacheEntry
		}
		items := make([]cacheItem, 0, len(s.storageCache))
		for path, entry := range s.storageCache {
			items = append(items, cacheItem{path, entry})
		}

		// Sort by lastAccess (oldest first) with sort.Slice
		sort.Slice(items, func(i, j int) bool {
			return items[i].entry.lastAccess.Before(items[j].entry.lastAccess)
		})

		// Evict oldest entries until we're under the limit
		numToEvict := len(s.storageCache) - s.maxCacheSize
		for i := 0; i < numToEvict && i < len(items); i++ {
			toClose = append(toClose, items[i].entry.store)
			delete(s.storageCache, items[i].path)
			s.metrics.RecordCacheEviction()
		}
	}

	// Unlock before closing to avoid holding lock during Close
	s.cacheMu.Unlock()

	// Close connections synchronously
	for _, store := range toClose {
		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close evicted storage: %v\n", err)
		}
	}
}

// getStorageForRequest returns the appropriate storage for the request
// If req.Cwd is set, it finds the database for that directory
// Otherwise, it uses the default storage
func (s *Server) getStorageForRequest(req *Request) (storage.Storage, error) {
	// If no cwd specified, use default storage
	if req.Cwd == "" {
		return s.storage, nil
	}

	// Find database for this cwd (to get the canonical repo root)
	dbPath := s.findDatabaseForCwd(req.Cwd)
	if dbPath == "" {
		return nil, fmt.Errorf("no .beads database found for path: %s", req.Cwd)
	}

	// Canonicalize key to repo root (parent of .beads directory)
	beadsDir := filepath.Dir(dbPath)
	repoRoot := filepath.Dir(beadsDir)

	// Check cache first with write lock (to avoid race on lastAccess update)
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if entry, ok := s.storageCache[repoRoot]; ok {
		// Check if DB file has been modified externally
		info, err := os.Stat(dbPath)
		if err == nil && !info.ModTime().Equal(entry.dbMtime) {
			// DB file changed - evict stale cache entry
			// Remove from cache first to prevent concurrent access
			delete(s.storageCache, repoRoot)
			atomic.AddInt64(&s.cacheMisses, 1)
			// Close storage after removing from cache (safe now)
			// Unlock briefly to avoid blocking during Close()
			s.cacheMu.Unlock()
			if err := entry.store.Close(); err != nil {
				// Log but don't fail - we'll reopen anyway
				fmt.Fprintf(os.Stderr, "Warning: failed to close stale cached storage: %v\n", err)
			}
			s.cacheMu.Lock()
			// Fall through to reopen
		} else if err == nil {
			// Cache hit - DB file unchanged
			entry.lastAccess = time.Now()
			atomic.AddInt64(&s.cacheHits, 1)
			return entry.store, nil
		} else {
			// Stat failed - evict and reopen
			// Remove from cache first to prevent concurrent access
			delete(s.storageCache, repoRoot)
			atomic.AddInt64(&s.cacheMisses, 1)
			// Close storage after removing from cache
			s.cacheMu.Unlock()
			if err := entry.store.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close cached storage: %v\n", err)
			}
			s.cacheMu.Lock()
			// Fall through to reopen
		}
	} else {
		atomic.AddInt64(&s.cacheMisses, 1)
	}

	// Open storage
	store, err := sqlite.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}

	// Get mtime for the newly opened DB
	info, err := os.Stat(dbPath)
	if err != nil {
		// If we can't stat, still cache it but with zero mtime (will invalidate on next check)
		info = nil
	}

	mtime := time.Time{}
	if info != nil {
		mtime = info.ModTime()
	}

	// Cache it with current timestamp and mtime
	s.storageCache[repoRoot] = &StorageCacheEntry{
		store:      store,
		lastAccess: time.Now(),
		dbMtime:    mtime,
	}

	// Enforce LRU immediately to prevent FD spikes between cleanup ticks
	needEvict := len(s.storageCache) > s.maxCacheSize
	s.cacheMu.Unlock()

	if needEvict {
		s.evictStaleStorage()
	}

	// Re-acquire lock for defer
	s.cacheMu.Lock()

	return store, nil
}

// findDatabaseForCwd walks up from cwd to find .beads/*.db
func (s *Server) findDatabaseForCwd(cwd string) string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}

	// Walk up directory tree
	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			// Found .beads/ directory, look for *.db files
			matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
			if err == nil && len(matches) > 0 {
				return matches[0]
			}
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}
		dir = parent
	}

	return ""
}
