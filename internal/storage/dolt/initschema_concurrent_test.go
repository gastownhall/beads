package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestConcurrentInitSchema verifies that concurrent initSchemaOnDB calls on a
// fresh database do not corrupt the schema. All DDL uses IF NOT EXISTS / ON
// DUPLICATE KEY so concurrent execution is idempotent.
func TestConcurrentInitSchema(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create a fresh database that has never been initialized.
	dbName := uniqueTestDBName(t)
	initDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	initDB, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("open init connection: %v", err)
	}
	defer initDB.Close()

	if _, err := initDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	// Open N independent sql.DB pools pointing at the fresh database.
	// Each simulates a separate bd process connecting simultaneously.
	const numConcurrent = 20
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()

	tmpDir, err := os.MkdirTemp("", "dolt-concurrent-init-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	var wg sync.WaitGroup
	errs := make(chan error, numConcurrent)

	// All goroutines are created before any of them open their connection, to
	// maximize the chance they all arrive at initSchemaOnDB simultaneously.
	ready := make(chan struct{})
	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			db, err := sql.Open("mysql", dsn)
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: open: %w", n, err)
				return
			}
			defer db.Close()
			db.SetMaxOpenConns(2)

			<-ready // wait for all goroutines to be ready

			if err := initSchemaOnDB(ctx, db); err != nil {
				errs <- fmt.Errorf("goroutine %d: initSchemaOnDB: %w", n, err)
			}
		}(i)
	}

	// Release all goroutines simultaneously to maximize contention.
	close(ready)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent init error: %v", err)
	}

	// Verify the schema was correctly initialized: check schema_version and
	// a representative set of tables.
	verifyDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open verify connection: %v", err)
	}
	defer verifyDB.Close()

	var maxVersion int
	if err := verifyDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion); err != nil {
		t.Fatalf("schema_migrations query failed after concurrent init: %v", err)
	}
	if maxVersion != schema.LatestVersion() {
		t.Errorf("max migration version = %d, want %d", maxVersion, schema.LatestVersion())
	}

	for _, table := range []string{"issues", "dependencies", "config", "comments"} {
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '%s' AND table_name = '%s'", dbName, table)
		if err := verifyDB.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Errorf("checking table %s: %v", table, err)
			continue
		}
		if count == 0 {
			t.Errorf("table %s missing after concurrent init", table)
		}
	}
}
