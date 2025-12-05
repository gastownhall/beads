package autoimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/memory"
)

// TestCheckStaleness_SymlinkedJSONL verifies that mtime detection uses the symlink's
// own mtime, not the target's mtime. This is critical for NixOS and similar systems
// where files may be symlinked to read-only locations.
//
// Behavior being tested:
// - When JSONL is a symlink, CheckStaleness should use os.Lstat (symlink mtime)
// - NOT os.Stat (which would follow the symlink and get target's mtime)
func TestCheckStaleness_SymlinkedJSONL(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the target JSONL file with old mtime
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(targetDir, "issues.jsonl")
	if err := os.WriteFile(targetPath, []byte(`{"id":"test-1"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set target's mtime to 1 hour ago
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(targetPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create the .beads directory structure with a symlink to the target
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// The symlink itself was just created (recent mtime)
	// The target file has old mtime (1 hour ago)
	// If we use os.Stat (follows symlink), we'd get the target's old mtime
	// If we use os.Lstat (symlink's own mtime), we'd get the recent mtime

	// Set last_import_time to 30 minutes ago (between target mtime and symlink mtime)
	importTime := time.Now().Add(-30 * time.Minute)
	store := memory.New("")
	ctx := context.Background()
	store.SetMetadata(ctx, "last_import_time", importTime.Format(time.RFC3339))

	dbPath := filepath.Join(beadsDir, "beads.db")

	// With correct behavior (os.Lstat):
	// - Symlink mtime: now (just created)
	// - Import time: 30 min ago
	// - Result: stale = true (symlink is newer than import)
	//
	// With incorrect behavior (os.Stat):
	// - Target mtime: 1 hour ago
	// - Import time: 30 min ago
	// - Result: stale = false (target is older than import) - WRONG!
	stale, err := CheckStaleness(ctx, store, dbPath)
	if err != nil {
		t.Fatalf("CheckStaleness failed: %v", err)
	}

	if !stale {
		t.Error("Expected stale=true when symlinked JSONL is newer than last import")
		t.Error("This indicates os.Stat is being used instead of os.Lstat")
		t.Error("os.Stat follows the symlink and returns target's mtime (old)")
		t.Error("os.Lstat returns the symlink's own mtime (recent)")
	}
}

// TestCheckStaleness_SymlinkedJSONL_NotStale verifies the inverse case:
// when the symlink itself is older than the last import, it should not be stale.
func TestCheckStaleness_SymlinkedJSONL_NotStale(t *testing.T) {
	tmpDir := t.TempDir()

	// Create target file
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(targetDir, "issues.jsonl")
	if err := os.WriteFile(targetPath, []byte(`{"id":"test-1"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// Set symlink's mtime to 1 hour ago
	oldTime := time.Now().Add(-1 * time.Hour)
	// Note: os.Chtimes follows symlinks, so we use os.Lchtimes if available
	// On most systems, symlink mtime is set at creation and can't be changed
	// So we'll set the import time to be in the future instead
	_ = oldTime

	// Set last_import_time to just now (after symlink creation)
	importTime := time.Now().Add(1 * time.Second)
	store := memory.New("")
	ctx := context.Background()
	store.SetMetadata(ctx, "last_import_time", importTime.Format(time.RFC3339))

	dbPath := filepath.Join(beadsDir, "beads.db")

	stale, err := CheckStaleness(ctx, store, dbPath)
	if err != nil {
		t.Fatalf("CheckStaleness failed: %v", err)
	}

	if stale {
		t.Error("Expected stale=false when last import is after symlink creation")
	}
}
