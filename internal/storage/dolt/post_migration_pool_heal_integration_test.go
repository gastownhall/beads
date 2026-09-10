//go:build integration

package dolt

import (
	"path/filepath"
	"testing"
)

// TestMigratingOpen_FirstReadSucceeds reproduces be-itm5 (be-jjv2 harness):
// a store open that applies migrations left the connection sitting in
// store.db pinned to the pre-migration Dolt session root. Its first
// statement read that stale root and failed with "table not found"; a
// FAILING query never advances the session root, so the error did not
// self-heal on retry — only an unrelated SUCCEEDING query (e.g. an
// information_schema probe) advanced it. See /var/tmp/be-jjv2-dump for the
// full evidence trail this test is derived from.
//
// The test issues exactly one query — the very first statement against the
// store after New() returns — because a second, unrelated query would mask
// the bug by advancing the session root itself.
func TestMigratingOpen_FirstReadSucceeds(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := testContext(t)
	defer cancel()

	dbName := uniqueTestDBName(t)
	runtimeDir := filepath.Join(t.TempDir(), "runtime")

	store, err := New(ctx, &Config{
		Path:            runtimeDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (migrating open): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The FIRST statement against the store after a migrating open. Do not
	// add any query before this one — a single successful statement (even an
	// information_schema probe) masks the bug entirely.
	rows, err := store.db.QueryContext(ctx, "SELECT `key` FROM config")
	if err != nil {
		t.Fatalf("first read after migrating open: %v (this is be-itm5: a connection "+
			"established before migrations ran stays pinned to the pre-migration "+
			"session root)", err)
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating config rows: %v", err)
	}
	if n == 0 {
		t.Fatalf("first read after migrating open returned 0 rows; want the migrated config seed data")
	}
}
