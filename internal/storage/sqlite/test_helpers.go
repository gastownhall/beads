package sqlite

import (
	"context"
	"testing"
)

// newTestStore creates a SQLiteStorage with issue_prefix configured (bd-166)
// This prevents "database not initialized" errors in tests
//
// Test Isolation Pattern (bd-2e80):
// By default, uses "file::memory:?mode=memory&cache=private" for proper test isolation.
// The standard ":memory:" creates a SHARED database across all tests in the same process,
// which can cause test interference and flaky behavior. The private mode ensures each
// test gets its own isolated in-memory database.
//
// To override (e.g., for file-based tests), pass a custom dbPath:
//   - For temp files: t.TempDir()+"/test.db"
//   - For shared memory (not recommended): ":memory:"
func newTestStore(t *testing.T, dbPath string) *SQLiteStorage {
	t.Helper()

	// Default to temp file for test isolation
	// File-based databases are more reliable than in-memory for connection pool scenarios
	if dbPath == "" {
		dbPath = t.TempDir() + "/test.db"
	}

	ctx := context.Background()
	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Fatalf("Failed to close test database: %v", cerr)
		}
	})

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	return store
}
