package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/pgdialect"
)

// TestInitSchema applies the embedded DDL into a throwaway schema on a live
// Postgres and asserts the core tables materialize. It is gated on
// BEADS_PG_TEST_URL (a pgx-parseable DSN) and skips otherwise.
func TestInitSchema(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping Postgres schema test")
	}

	schema := fmt.Sprintf("bd_schema_test_%d", time.Now().UnixNano())

	// DDL runs over a RAW (non-translating) DB; assertions below use ? bindings
	// and so need the translating DB.
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("openraw: %v", err)
	}
	db, err := pgdialect.Open(url, schema)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Registered first so they run last (cleanups are LIFO): the schema drop
	// below still has open connections when it runs.
	t.Cleanup(func() { _ = raw.Close() })
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()

	if err := InitSchema(ctx, raw, schema); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop schema %q: %v", schema, err)
		}
	})

	for _, table := range []string{"issues", "dependencies"} {
		if !tableExists(ctx, t, db, schema, table) {
			t.Errorf("expected table %q to exist in schema %q", table, schema)
		}
	}
}

func tableExists(ctx context.Context, t *testing.T, db *sql.DB, schema, table string) bool {
	t.Helper()
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ?`,
		schema, table).Scan(&count)
	if err != nil {
		t.Fatalf("query information_schema.tables for %q: %v", table, err)
	}
	return count > 0
}
