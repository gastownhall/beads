package rpc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// newTestStore creates a SQLite store with issue_prefix configured (bd-166)
// This prevents "database not initialized" errors in tests
func newTestStore(t *testing.T, dbPath string) *sqlite.SQLiteStorage {
	t.Helper()

	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	return store
}

func newTestSocketPath(t *testing.T) string {
	t.Helper()

	// On unix, AF_UNIX socket paths have small length limits (notably on darwin).
	// Prefer a short base dir when available.
	if runtime.GOOS != "windows" {
		d, err := os.MkdirTemp("/tmp", "beads-sock-")
		if err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(d) })
			return filepath.Join(d, "rpc.sock")
		}
	}

	return filepath.Join(t.TempDir(), "rpc.sock")
}
