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
)

// TestConcurrentInitSchema verifies that concurrent initSchemaOnDB calls on a
// fresh database do not corrupt the Dolt journal. Without the GET_LOCK advisory
// lock, 20+ concurrent processes running DDL simultaneously on a fresh DB
// reliably produce CRC/journal corruption (see GH#2672).
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
	initDSN := fmt.Sprintf("root@tcp(127.0.0.1:%d)/", testServerPort)
	initDB, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("open init connection: %v", err)
	}
	defer initDB.Close()

	if _, err := initDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		// Skip DROP — rapid create/drop cycles can crash the Dolt container.
		// The orphan is cleaned up when the container terminates.
	})

	// Open N independent sql.DB pools pointing at the fresh database.
	// Each simulates a separate bd process connecting simultaneously.
	const numConcurrent = 20
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s?parseTime=true", testServerPort, dbName)

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

	var version int
	if err := verifyDB.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'schema_version'").Scan(&version); err != nil {
		t.Fatalf("schema_version not found after concurrent init: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, currentSchemaVersion)
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
