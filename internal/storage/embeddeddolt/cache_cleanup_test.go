package embeddeddolt

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanGitRemoteCacheGarbage(t *testing.T) {
	// Reset the throttle so the test always runs the cleanup.
	cacheCleanupThrottle.mu.Lock()
	cacheCleanupThrottle.lastRun = time.Time{}
	cacheCleanupThrottle.mu.Unlock()

	tmpDir := t.TempDir()
	packDir := filepath.Join(tmpDir, "testdb", ".dolt", "git-remote-cache", "abc123", "repo.git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0755); err != nil {
		t.Fatal(err)
	}

	staleFile := filepath.Join(packDir, "tmp_pack_STALE1")
	if err := os.WriteFile(staleFile, []byte("stale data"), 0444); err != nil {
		t.Fatal(err)
	}
	// Backdate the file well past the min age.
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(staleFile, old, old); err != nil {
		t.Fatal(err)
	}

	staleIdx := filepath.Join(packDir, "tmp_idx_STALE2")
	if err := os.WriteFile(staleIdx, []byte("stale idx"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleIdx, old, old); err != nil {
		t.Fatal(err)
	}

	recentFile := filepath.Join(packDir, "tmp_pack_RECENT")
	if err := os.WriteFile(recentFile, []byte("in progress"), 0444); err != nil {
		t.Fatal(err)
	}

	finalPack := filepath.Join(packDir, "pack-abc123.pack")
	if err := os.WriteFile(finalPack, []byte("real pack"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(finalPack, old, old); err != nil {
		t.Fatal(err)
	}

	s := &EmbeddedDoltStore{
		dataDir:  tmpDir,
		database: "testdb",
	}
	s.cleanGitRemoteCacheGarbage()

	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Errorf("stale tmp_pack file should have been deleted, but still exists")
	}
	if _, err := os.Stat(staleIdx); !os.IsNotExist(err) {
		t.Errorf("stale tmp_idx file should have been deleted, but still exists")
	}
	if _, err := os.Stat(recentFile); err != nil {
		t.Errorf("recent tmp_pack file should NOT have been deleted: %v", err)
	}
	if _, err := os.Stat(finalPack); err != nil {
		t.Errorf("finalized pack file should NOT have been deleted: %v", err)
	}
}

func TestCleanGitRemoteCacheGarbage_Throttled(t *testing.T) {
	// Reset then immediately mark as just-run.
	cacheCleanupThrottle.mu.Lock()
	cacheCleanupThrottle.lastRun = time.Now()
	cacheCleanupThrottle.mu.Unlock()

	tmpDir := t.TempDir()
	packDir := filepath.Join(tmpDir, "testdb", ".dolt", "git-remote-cache", "abc123", "repo.git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0755); err != nil {
		t.Fatal(err)
	}

	staleFile := filepath.Join(packDir, "tmp_pack_STALE1")
	if err := os.WriteFile(staleFile, []byte("stale"), 0444); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(staleFile, old, old); err != nil {
		t.Fatal(err)
	}

	s := &EmbeddedDoltStore{
		dataDir:  tmpDir,
		database: "testdb",
	}
	s.cleanGitRemoteCacheGarbage()

	if _, err := os.Stat(staleFile); os.IsNotExist(err) {
		t.Errorf("throttled cleanup should NOT have deleted the file, but it was deleted")
	}
}

func TestCleanGitRemoteCacheGarbage_NoCacheDir(t *testing.T) {
	// Reset throttle.
	cacheCleanupThrottle.mu.Lock()
	cacheCleanupThrottle.lastRun = time.Time{}
	cacheCleanupThrottle.mu.Unlock()

	tmpDir := t.TempDir()
	s := &EmbeddedDoltStore{
		dataDir:  tmpDir,
		database: "testdb",
	}
	// Should not panic when the cache directory doesn't exist.
	s.cleanGitRemoteCacheGarbage()
}
