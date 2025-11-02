package rpc

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// newTestStore creates a SQLite store with issue_prefix configured (bd-166)
// This prevents "database not initialized" errors in tests
func newTestStore(t *testing.T, dbPath string) *sqlite.SQLiteStorage {
	t.Helper()
	
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	
	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	
	return store
}
