package dolt

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// TestFreshDB_PoolReadsSchemaMigrations reproduces a pooled-connection
// poisoning bug: on a brand-new database, the migration cursor's probe query
// runs against schema_migrations before the table exists, and Dolt pins that
// pooled session's catalog to its pre-statement snapshot on the resulting
// failure. Migrations then create the table on a different connection, but a
// caller who later reuses the poisoned connection still sees "table not
// found: schema_migrations" even though the table is really there.
//
// A single query can pass by luck if it happens to land on a connection that
// never saw the pre-table failure, so this loops enough times to cycle
// through the whole pool rather than trust one draw.
func TestFreshDB_PoolReadsSchemaMigrations(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbName := uniqueTestDBName(t)
	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	for i := 0; i < defaultMaxOpenConns*2; i++ {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
			t.Fatalf("store's own pool could not read schema_migrations on iteration %d (fresh DB immediately after New()): %v", i, err)
		}
		if count == 0 {
			t.Fatalf("schema_migrations has no rows on iteration %d — migrations did not record their version", i)
		}
	}
}
