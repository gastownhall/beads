package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
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

// TestInitSchemaConcurrent guards process-level startup: Gas City and other
// orchestrators may launch several short-lived bd commands for one workspace at
// once. Every command opens the backend and calls InitSchema, so the bootstrap
// DDL must serialize across independent PostgreSQL sessions.
func TestInitSchemaConcurrent(t *testing.T) {
	pgURL := os.Getenv("BEADS_PG_TEST_URL")
	if pgURL == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping concurrent Postgres schema test")
	}
	schema := fmt.Sprintf("bd_schema_concurrent_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := pgdialect.OpenRaw(pgURL, schema)
			if err != nil {
				errs <- err
				return
			}
			defer raw.Close()
			<-start
			errs <- InitSchema(ctx, raw, schema)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent InitSchema: %v", err)
		}
	}

	raw, err := pgdialect.OpenRaw(pgURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
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
