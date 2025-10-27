package rpc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestStorageCacheEviction_TTL(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server with short TTL for testing
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	server.cacheTTL = 100 * time.Millisecond // Short TTL for testing
	defer server.Stop()

	// Create two test databases
	db1 := filepath.Join(tmpDir, "repo1", ".beads", "issues.db")
	os.MkdirAll(filepath.Dir(db1), 0755)
	store1, err := sqlite.New(db1)
	if err != nil {
		t.Fatal(err)
	}
	store1.Close()

	db2 := filepath.Join(tmpDir, "repo2", ".beads", "issues.db")
	os.MkdirAll(filepath.Dir(db2), 0755)
	store2, err := sqlite.New(db2)
	if err != nil {
		t.Fatal(err)
	}
	store2.Close()

	// Access both repos to populate cache
	req1 := &Request{Cwd: filepath.Join(tmpDir, "repo1")}
	_, err = server.getStorageForRequest(req1)
	if err != nil {
		t.Fatal(err)
	}

	req2 := &Request{Cwd: filepath.Join(tmpDir, "repo2")}
	_, err = server.getStorageForRequest(req2)
	if err != nil {
		t.Fatal(err)
	}

	// Verify both are cached
	server.cacheMu.RLock()
	cacheSize := len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 2 {
		t.Fatalf("expected 2 cached entries, got %d", cacheSize)
	}

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Run eviction
	server.evictStaleStorage()

	// Verify both entries were evicted
	server.cacheMu.RLock()
	cacheSize = len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 0 {
		t.Fatalf("expected 0 cached entries after TTL eviction, got %d", cacheSize)
	}
}

func TestStorageCacheEviction_LRU(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server with small cache size
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	server.maxCacheSize = 2         // Only keep 2 entries
	server.cacheTTL = 1 * time.Hour // Long TTL so we test LRU
	defer server.Stop()

	// Create three test databases
	for i := 1; i <= 3; i++ {
		dbPath := filepath.Join(tmpDir, "repo"+string(rune('0'+i)), ".beads", "issues.db")
		os.MkdirAll(filepath.Dir(dbPath), 0755)
		store, err := sqlite.New(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
	}

	// Access repos 1 and 2
	req1 := &Request{Cwd: filepath.Join(tmpDir, "repo1")}
	_, err = server.getStorageForRequest(req1)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // Ensure different timestamps

	req2 := &Request{Cwd: filepath.Join(tmpDir, "repo2")}
	_, err = server.getStorageForRequest(req2)
	if err != nil {
		t.Fatal(err)
	}

	// Verify 2 entries cached
	server.cacheMu.RLock()
	cacheSize := len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 2 {
		t.Fatalf("expected 2 cached entries, got %d", cacheSize)
	}

	// Access repo 3, which should trigger LRU eviction of repo1 (oldest)
	req3 := &Request{Cwd: filepath.Join(tmpDir, "repo3")}
	_, err = server.getStorageForRequest(req3)
	if err != nil {
		t.Fatal(err)
	}

	// Run eviction to enforce max cache size
	server.evictStaleStorage()

	// Should still have 2 entries
	server.cacheMu.RLock()
	cacheSize = len(server.storageCache)
	_, hasRepo1 := server.storageCache[filepath.Join(tmpDir, "repo1")]
	_, hasRepo2 := server.storageCache[filepath.Join(tmpDir, "repo2")]
	_, hasRepo3 := server.storageCache[filepath.Join(tmpDir, "repo3")]
	server.cacheMu.RUnlock()

	if cacheSize != 2 {
		t.Fatalf("expected 2 cached entries after LRU eviction, got %d", cacheSize)
	}

	// Repo1 should be evicted (oldest), repo2 and repo3 should remain
	if hasRepo1 {
		t.Error("repo1 should have been evicted (oldest)")
	}
	if !hasRepo2 {
		t.Error("repo2 should still be cached")
	}
	if !hasRepo3 {
		t.Error("repo3 should be cached")
	}
}

func TestStorageCacheEviction_LastAccessUpdate(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	defer server.Stop()

	// Create test database
	dbPath := filepath.Join(tmpDir, "repo1", ".beads", "issues.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// First access
	req := &Request{Cwd: filepath.Join(tmpDir, "repo1")}
	_, err = server.getStorageForRequest(req)
	if err != nil {
		t.Fatal(err)
	}

	// Get initial lastAccess time
	server.cacheMu.RLock()
	entry := server.storageCache[filepath.Join(tmpDir, "repo1")]
	initialTime := entry.lastAccess
	server.cacheMu.RUnlock()

	// Wait a bit
	time.Sleep(50 * time.Millisecond)

	// Access again
	_, err = server.getStorageForRequest(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify lastAccess was updated
	server.cacheMu.RLock()
	entry = server.storageCache[filepath.Join(tmpDir, "repo1")]
	updatedTime := entry.lastAccess
	server.cacheMu.RUnlock()

	if !updatedTime.After(initialTime) {
		t.Errorf("lastAccess should be updated on cache hit, initial: %v, updated: %v", initialTime, updatedTime)
	}
}

func TestStorageCacheEviction_EnvVars(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Set env vars
	os.Setenv("BEADS_DAEMON_MAX_CACHE_SIZE", "100")
	os.Setenv("BEADS_DAEMON_CACHE_TTL", "1h30m")
	defer os.Unsetenv("BEADS_DAEMON_MAX_CACHE_SIZE")
	defer os.Unsetenv("BEADS_DAEMON_CACHE_TTL")

	// Create server
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	defer server.Stop()

	// Verify config was parsed
	if server.maxCacheSize != 100 {
		t.Errorf("expected maxCacheSize=100, got %d", server.maxCacheSize)
	}
	expectedTTL := 90 * time.Minute
	if server.cacheTTL != expectedTTL {
		t.Errorf("expected cacheTTL=%v, got %v", expectedTTL, server.cacheTTL)
	}
}

func TestStorageCacheEviction_CleanupOnStop(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)

	// Create test database and populate cache
	dbPath := filepath.Join(tmpDir, "repo1", ".beads", "issues.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	req := &Request{Cwd: filepath.Join(tmpDir, "repo1")}
	_, err = server.getStorageForRequest(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify cached
	server.cacheMu.RLock()
	cacheSize := len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 1 {
		t.Fatalf("expected 1 cached entry, got %d", cacheSize)
	}

	// Stop server
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}

	// Verify cache was cleared
	server.cacheMu.RLock()
	cacheSize = len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 0 {
		t.Errorf("expected cache to be cleared on stop, got %d entries", cacheSize)
	}
}

func TestStorageCacheEviction_CanonicalKey(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	defer server.Stop()

	// Create test database
	dbPath := filepath.Join(tmpDir, "repo1", ".beads", "issues.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Access from different subdirectories of the same repo
	req1 := &Request{Cwd: filepath.Join(tmpDir, "repo1")}
	_, err = server.getStorageForRequest(req1)
	if err != nil {
		t.Fatal(err)
	}

	req2 := &Request{Cwd: filepath.Join(tmpDir, "repo1", "subdir1")}
	_, err = server.getStorageForRequest(req2)
	if err != nil {
		t.Fatal(err)
	}

	req3 := &Request{Cwd: filepath.Join(tmpDir, "repo1", "subdir1", "subdir2")}
	_, err = server.getStorageForRequest(req3)
	if err != nil {
		t.Fatal(err)
	}

	// Should only have one cache entry (all pointing to same repo root)
	server.cacheMu.RLock()
	cacheSize := len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 1 {
		t.Errorf("expected 1 cached entry (canonical key), got %d", cacheSize)
	}
}

func TestStorageCacheEviction_ImmediateLRU(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server with max cache size of 2
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	server.maxCacheSize = 2
	server.cacheTTL = 1 * time.Hour // Long TTL
	defer server.Stop()

	// Create 3 test databases
	for i := 1; i <= 3; i++ {
		dbPath := filepath.Join(tmpDir, fmt.Sprintf("repo%d", i), ".beads", "issues.db")
		os.MkdirAll(filepath.Dir(dbPath), 0755)
		store, err := sqlite.New(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
	}

	// Access all 3 repos
	for i := 1; i <= 3; i++ {
		req := &Request{Cwd: filepath.Join(tmpDir, fmt.Sprintf("repo%d", i))}
		_, err = server.getStorageForRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	// Cache should never exceed maxCacheSize (immediate LRU enforcement)
	server.cacheMu.RLock()
	cacheSize := len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize > server.maxCacheSize {
		t.Errorf("cache size %d exceeds max %d (immediate LRU not enforced)", cacheSize, server.maxCacheSize)
	}
}

func TestStorageCacheEviction_InvalidTTL(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Set invalid TTL
	os.Setenv("BEADS_DAEMON_CACHE_TTL", "-5m")
	defer os.Unsetenv("BEADS_DAEMON_CACHE_TTL")

	// Create server
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	defer server.Stop()

	// Should fall back to default (30 minutes)
	expectedTTL := 30 * time.Minute
	if server.cacheTTL != expectedTTL {
		t.Errorf("expected TTL to fall back to %v for invalid value, got %v", expectedTTL, server.cacheTTL)
	}
}

func TestStorageCacheEviction_ReopenAfterEviction(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server with short TTL
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)
	server.cacheTTL = 50 * time.Millisecond
	defer server.Stop()

	// Create test database
	dbPath := filepath.Join(tmpDir, "repo1", ".beads", "issues.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Access repo
	req := &Request{Cwd: filepath.Join(tmpDir, "repo1")}
	_, err = server.getStorageForRequest(req)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Evict
	server.evictStaleStorage()

	// Verify evicted
	server.cacheMu.RLock()
	cacheSize := len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 0 {
		t.Fatalf("expected cache to be empty after eviction, got %d", cacheSize)
	}

	// Access again - should cleanly re-open
	_, err = server.getStorageForRequest(req)
	if err != nil {
		t.Fatalf("failed to re-open after eviction: %v", err)
	}

	// Verify re-cached
	server.cacheMu.RLock()
	cacheSize = len(server.storageCache)
	server.cacheMu.RUnlock()
	if cacheSize != 1 {
		t.Errorf("expected 1 cached entry after re-open, got %d", cacheSize)
	}
}

func TestStorageCacheEviction_StopIdempotent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main DB
	mainDB := filepath.Join(tmpDir, "main.db")
	mainStore, err := sqlite.New(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mainStore.Close()

	// Create server
	socketPath := filepath.Join(tmpDir, "test.sock")
	server := NewServer(socketPath, mainStore, tmpDir, mainDB)

	// Stop multiple times - should not panic
	if err := server.Stop(); err != nil {
		t.Fatalf("first Stop failed: %v", err)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("second Stop failed: %v", err)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("third Stop failed: %v", err)
	}
}
