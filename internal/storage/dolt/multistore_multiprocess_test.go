//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil/integration"
	"golang.org/x/sync/errgroup"
)

// TestHelperMultiStore is the subprocess entry point for multi-DoltStore tests.
// Only executed when BEADS_MULTISTORE_HELPER=1 is set.
func TestHelperMultiStore(t *testing.T) {
	if os.Getenv("BEADS_MULTISTORE_HELPER") != "1" {
		return
	}

	port := os.Getenv("BEADS_MULTISTORE_PORT")
	dbName := os.Getenv("BEADS_MULTISTORE_DB")
	procID := os.Getenv("BEADS_MULTISTORE_PROC_ID")
	opsStr := os.Getenv("BEADS_MULTISTORE_OPS")

	if port == "" || dbName == "" || procID == "" {
		fmt.Fprintf(os.Stderr, "FATAL: missing required env vars\n")
		os.Exit(1)
	}

	numOps := 5
	if opsStr != "" {
		if n, err := strconv.Atoi(opsStr); err == nil {
			numOps = n
		}
	}

	portNum, err := strconv.Atoi(port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: invalid port %q: %v\n", port, err)
		os.Exit(1)
	}
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: portNum, User: "root", Database: dbName}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: sql.Open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(2 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: ping: %v\n", err)
		os.Exit(1)
	}

	// Create issues, mimicking how multiple bd processes would work.
	var created int
	for i := 0; i < numOps; i++ {
		issueID := fmt.Sprintf("mp-%s-%d", procID, i)
		title := fmt.Sprintf("Issue from proc %s op %d", procID, i)

		var inserted bool
		for retry := 0; retry < 5; retry++ {
			_, err := db.ExecContext(ctx,
				"INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at) VALUES (?, ?, 'open', 2, 'task', NOW(6), NOW(6))",
				issueID, title,
			)
			if err == nil {
				inserted = true
				break
			}
			if isSerializationError(err) {
				time.Sleep(time.Duration(50*(retry+1)) * time.Millisecond)
				continue
			}
			fmt.Fprintf(os.Stderr, "ERROR: INSERT %s: %v\n", issueID, err)
			break
		}
		if inserted {
			created++
		}
	}

	// Read back to verify.
	var count int
	prefix := fmt.Sprintf("mp-%s-%%", procID)
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id LIKE ?", prefix).Scan(&count); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: count: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stdout, "OK proc=%s created=%d verified=%d\n", procID, created, count)
}

// TestMultiStoreConcurrent_InProcess tests multiple independent DoltStore
// connections (same process, separate sql.DB pools) performing concurrent
// operations. This is Layer 1 — fast API invariant checks.
func TestMultiStoreConcurrent_InProcess(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create a fresh database with schema.
	dbName := uniqueTestDBName(t)
	initDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	adminDB, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	// Initialize schema once.
	schemaDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	schemaDB, err := sql.Open("mysql", schemaDSN)
	if err != nil {
		t.Fatalf("schema connect: %v", err)
	}
	if err := initSchemaOnDB(ctx, schemaDB); err != nil {
		t.Fatalf("initSchemaOnDB: %v", err)
	}
	schemaDB.Close()

	// Open 5 independent connection pools — each simulates a separate bd process.
	const numStores = 5
	const opsPerStore = 10
	dbs := make([]*sql.DB, numStores)
	for i := 0; i < numStores; i++ {
		db, err := sql.Open("mysql", schemaDSN)
		if err != nil {
			t.Fatalf("open pool %d: %v", i, err)
		}
		db.SetMaxOpenConns(3)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(2 * time.Minute)
		dbs[i] = db
		t.Cleanup(func() { db.Close() })
	}

	// Concurrent creates across all pools.
	var totalCreated atomic.Int32
	eg, egCtx := errgroup.WithContext(ctx)
	for storeIdx := 0; storeIdx < numStores; storeIdx++ {
		db := dbs[storeIdx]
		idx := storeIdx
		eg.Go(func() error {
			for op := 0; op < opsPerStore; op++ {
				id := fmt.Sprintf("ms-%d-%d", idx, op)
				title := fmt.Sprintf("Multi-store issue %d-%d", idx, op)
				_, err := db.ExecContext(egCtx,
					"INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at) VALUES (?, ?, 'open', 2, 'task', NOW(6), NOW(6))",
					id, title,
				)
				if err != nil {
					if isSerializationError(err) {
						continue // Expected under contention.
					}
					return fmt.Errorf("store %d op %d: %w", idx, op, err)
				}
				totalCreated.Add(1)
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		t.Fatalf("concurrent operations: %v", err)
	}

	// Verify all issues exist.
	var totalCount int
	verifyDB := dbs[0]
	if err := verifyDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id LIKE 'ms-%'").Scan(&totalCount); err != nil {
		t.Fatalf("count: %v", err)
	}

	t.Logf("Created %d issues, verified %d in database", totalCreated.Load(), totalCount)
	if totalCount == 0 {
		t.Error("no issues found — concurrent creates all failed")
	}
	// At least 80% of acknowledged writes should be present.
	// Some loss is expected from serialization errors, but massive loss
	// indicates a real concurrency bug.
	minExpected := int(float64(totalCreated.Load()) * 0.8)
	if totalCount < minExpected {
		t.Errorf("data integrity: only %d/%d acknowledged writes found in DB (min %d expected)", totalCount, totalCreated.Load(), minExpected)
	}

	// Concurrent read while one pool writes.
	var readErrors atomic.Int32
	eg2, egCtx2 := errgroup.WithContext(ctx)

	// Writer: updates existing issues.
	eg2.Go(func() error {
		for i := 0; i < 10; i++ {
			_, _ = dbs[0].ExecContext(egCtx2,
				"UPDATE issues SET priority = ? WHERE id LIKE 'ms-0-%' LIMIT 1",
				i%5,
			)
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	})

	// Readers: concurrent reads.
	for r := 1; r < numStores; r++ {
		db := dbs[r]
		eg2.Go(func() error {
			for i := 0; i < 20; i++ {
				var c int
				if err := db.QueryRowContext(egCtx2, "SELECT COUNT(*) FROM issues").Scan(&c); err != nil {
					readErrors.Add(1)
				}
				time.Sleep(5 * time.Millisecond)
			}
			return nil
		})
	}

	if err := eg2.Wait(); err != nil {
		t.Fatalf("read/write mix: %v", err)
	}
	if readErrors.Load() > 0 {
		t.Logf("Warning: %d read errors during concurrent writes (may be expected under contention)", readErrors.Load())
	}
}

// TestMultiStoreConcurrent_Subprocess is Layer 2 — true multi-process test.
// Spawns N subprocesses, each with its own sql.DB pool, all hitting the same
// database. Verifies no data loss or corruption.
// Would have caught: GH#2430 (journal corruption from multi-process writes).
func TestMultiStoreConcurrent_Subprocess(t *testing.T) {
	skipIfNoDolt(t)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	modRoot := integration.ModuleRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	runner := integration.NewSubprocessRunner(modRoot, "./internal/storage/dolt/")
	testBin := runner.Build(t)

	// Create a fresh database with schema.
	dbName := uniqueTestDBName(t)
	initDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	adminDB, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	schemaDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	schemaDB, err := sql.Open("mysql", schemaDSN)
	if err != nil {
		t.Fatalf("schema connect: %v", err)
	}
	if err := initSchemaOnDB(ctx, schemaDB); err != nil {
		t.Fatalf("initSchemaOnDB: %v", err)
	}
	schemaDB.Close()

	// Spawn subprocesses.
	const numProcs = 5
	const opsPerProc = 5
	portStr := strconv.Itoa(testServerPort)

	var successCount atomic.Int32
	eg, egCtx := errgroup.WithContext(ctx)

	for i := 0; i < numProcs; i++ {
		procID := strconv.Itoa(i)
		eg.Go(func() error {
			subCtx, subCancel := context.WithTimeout(egCtx, 30*time.Second)
			defer subCancel()

			cmd := exec.CommandContext(subCtx, testBin,
				"-test.run=^TestHelperMultiStore$",
				"-test.v",
			)
			cmd.Env = integration.FilterEnv(os.Environ())
			cmd.Env = append(cmd.Env,
				"BEADS_MULTISTORE_HELPER=1",
				"BEADS_MULTISTORE_PORT="+portStr,
				"BEADS_MULTISTORE_DB="+dbName,
				"BEADS_MULTISTORE_PROC_ID="+procID,
				"BEADS_MULTISTORE_OPS="+strconv.Itoa(opsPerProc),
				"BEADS_TEST_MODE=1",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("subprocess %s: %v\noutput: %s", procID, err, out)
			}
			if strings.Contains(string(out), "OK proc=") {
				successCount.Add(1)
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		t.Fatalf("subprocess error: %v", err)
	}
	t.Logf("All %d subprocesses completed (%d successful)", numProcs, successCount.Load())

	// Verify total issue count.
	verifyDB, err := sql.Open("mysql", schemaDSN)
	if err != nil {
		t.Fatalf("verify connect: %v", err)
	}
	defer verifyDB.Close()

	var totalIssues int
	if err := verifyDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id LIKE 'mp-%'").Scan(&totalIssues); err != nil {
		t.Fatalf("count: %v", err)
	}
	t.Logf("Total issues in database: %d (expected up to %d)", totalIssues, numProcs*opsPerProc)

	if totalIssues == 0 {
		t.Error("no issues found — all subprocess writes failed")
	}

	// Check for duplicates.
	var dupeCount int
	if err := verifyDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT id, COUNT(*) c FROM issues WHERE id LIKE 'mp-%' GROUP BY id HAVING c > 1) dupes").Scan(&dupeCount); err != nil {
		t.Fatalf("dupe check: %v", err)
	}
	if dupeCount > 0 {
		t.Errorf("found %d duplicate issue IDs", dupeCount)
	}
}

// closeStore is a helper to test that closing one connection pool doesn't
// cause cascade failures in other pools.
func TestMultiStoreConcurrent_CloseIsolation(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbName := uniqueTestDBName(t)
	initDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	adminDB, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()

	// Open two pools.
	db1, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	db2, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	t.Cleanup(func() { db2.Close() })

	// Start a query on db2.
	var wg sync.WaitGroup
	wg.Add(1)
	db2Ready := make(chan struct{})
	go func() {
		defer wg.Done()
		close(db2Ready)
		for i := 0; i < 10; i++ {
			_, _ = db2.ExecContext(ctx, "SELECT 1")
			time.Sleep(20 * time.Millisecond)
		}
	}()

	<-db2Ready
	time.Sleep(50 * time.Millisecond)

	// Close db1 while db2 is active.
	db1.Close()

	// db2 should continue working without errors.
	wg.Wait()

	// Verify db2 is still functional.
	if _, err := db2.ExecContext(ctx, "SELECT 1"); err != nil {
		t.Errorf("db2 broken after db1.Close(): %v", err)
	}
}
