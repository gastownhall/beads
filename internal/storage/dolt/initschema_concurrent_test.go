package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	acquireAllTestSlots()
	t.Cleanup(releaseAllTestSlots)

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

			if _, err := initSchemaOnDBWithRetry(ctx, db); err != nil {
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

func TestInitSchemaBlocksOnMigrationLock(t *testing.T) {
	skipIfNoDolt(t)
	acquireAllTestSlots()
	t.Cleanup(releaseAllTestSlots)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	lockHolder, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open lock holder: %v", err)
	}
	defer lockHolder.Close()

	lockConn, err := lockHolder.Conn(ctx)
	if err != nil {
		t.Fatalf("pin lock holder: %v", err)
	}
	defer lockConn.Close()

	var lockHolderID int64
	if err := lockConn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&lockHolderID); err != nil {
		t.Fatalf("read lock holder connection id: %v", err)
	}

	lockName := schema.MigrationLockName(dbName)
	var locked int
	if err := lockConn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&locked); err != nil {
		t.Fatalf("hold migration lock: %v", err)
	}
	if locked != 1 {
		t.Fatalf("GET_LOCK returned %d, want 1", locked)
	}

	initDB2, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open init db: %v", err)
	}
	defer initDB2.Close()
	initDB2.SetMaxOpenConns(2)

	errCh := make(chan error, 1)
	go func() {
		_, err := initSchemaOnDBWithRetry(ctx, initDB2)
		errCh <- err
	}()

	waitForMigrationLockWaiter(t, ctx, initDB, lockHolderID, errCh)

	var released int
	if err := lockConn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil {
		t.Fatalf("release migration lock: %v", err)
	}
	if released != 1 {
		t.Fatalf("RELEASE_LOCK returned %d, want 1", released)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("initSchemaOnDB after releasing migration lock: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("initSchemaOnDB did not complete after releasing migration lock")
	}
}

func TestInitSchemaCanceledLockWaitDoesNotBlockFutureInit(t *testing.T) {
	skipIfNoDolt(t)
	acquireAllTestSlots()
	t.Cleanup(releaseAllTestSlots)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	lockHolder, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open lock holder: %v", err)
	}
	defer lockHolder.Close()

	lockConn, err := lockHolder.Conn(ctx)
	if err != nil {
		t.Fatalf("pin lock holder: %v", err)
	}
	defer lockConn.Close()

	var lockHolderID int64
	if err := lockConn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&lockHolderID); err != nil {
		t.Fatalf("read lock holder connection id: %v", err)
	}

	lockName := schema.MigrationLockName(dbName)
	var locked int
	if err := lockConn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&locked); err != nil {
		t.Fatalf("hold migration lock: %v", err)
	}
	if locked != 1 {
		t.Fatalf("GET_LOCK returned %d, want 1", locked)
	}

	blockedDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open blocked db: %v", err)
	}
	defer blockedDB.Close()

	callerCtx, callerCancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		_, err := initSchemaOnDBWithRetry(callerCtx, blockedDB)
		errCh <- err
	}()

	waitForMigrationLockWaiter(t, ctx, initDB, lockHolderID, errCh)
	callerCancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("initSchemaOnDB succeeded after caller context was canceled while waiting for the migration lock")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("initSchemaOnDB did not return after caller context cancellation")
	}

	var released int
	if err := lockConn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil {
		t.Fatalf("release migration lock: %v", err)
	}
	if released != 1 {
		t.Fatalf("RELEASE_LOCK returned %d, want 1", released)
	}

	secondDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open second db: %v", err)
	}
	defer secondDB.Close()

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer verifyCancel()
	if _, err := initSchemaOnDBWithRetry(verifyCtx, secondDB); err != nil {
		t.Fatalf("second initSchemaOnDB after canceled lock wait: %v", err)
	}
}

func TestMigrationLockReleaseIgnoresCanceledCallerContext(t *testing.T) {
	skipIfNoDolt(t)
	acquireAllTestSlots()
	t.Cleanup(releaseAllTestSlots)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer setupCancel()

	dbName := uniqueTestDBName(t)
	initDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	initDB, err := sql.Open("mysql", initDSN)
	if err != nil {
		t.Fatalf("open init connection: %v", err)
	}
	defer initDB.Close()

	if _, err := initDB.ExecContext(setupCtx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open lock db: %v", err)
	}
	defer db.Close()

	callerCtx, callerCancel := context.WithCancel(context.Background())
	conn, err := db.Conn(callerCtx)
	if err != nil {
		t.Fatalf("pin lock conn: %v", err)
	}
	defer conn.Close()

	lockName := schema.MigrationLockName(dbName)
	if err := schema.AcquireMigrationLock(callerCtx, conn, lockName); err != nil {
		t.Fatalf("acquire migration lock: %v", err)
	}
	callerCancel()

	if err := schema.ReleaseMigrationLock(conn, lockName); err != nil {
		t.Fatalf("release migration lock with canceled caller context: %v", err)
	}

	secondDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open second db: %v", err)
	}
	defer secondDB.Close()

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer verifyCancel()
	if _, err := initSchemaOnDBWithRetry(verifyCtx, secondDB); err != nil {
		t.Fatalf("second initSchemaOnDB after canceled-context release: %v", err)
	}
}

func waitForMigrationLockWaiter(t *testing.T, ctx context.Context, observer *sql.DB, holderID int64, errCh <-chan error) {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-errCh:
			t.Fatalf("initSchemaOnDB completed before a GET_LOCK waiter was observed: %v", err)
		default:
		}

		visible, err := migrationLockWaiterVisible(waitCtx, observer, holderID)
		if err != nil {
			t.Fatalf("observe migration lock waiter: %v", err)
		}
		if visible {
			return
		}

		select {
		case <-waitCtx.Done():
			t.Fatal("timed out waiting for initSchemaOnDB to appear as a GET_LOCK waiter")
		case <-ticker.C:
		}
	}
}

func migrationLockWaiterVisible(ctx context.Context, observer *sql.DB, holderID int64) (bool, error) {
	rows, err := observer.QueryContext(ctx, "SHOW PROCESSLIST")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}

	idIndex := -1
	infoIndex := -1
	for i, column := range columns {
		switch strings.ToLower(column) {
		case "id":
			idIndex = i
		case "info":
			infoIndex = i
		}
	}
	if idIndex < 0 || infoIndex < 0 {
		return false, fmt.Errorf("SHOW PROCESSLIST columns missing id/info: %v", columns)
	}

	values := make([]sql.NullString, len(columns))
	scanArgs := make([]any, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	for rows.Next() {
		for i := range values {
			values[i] = sql.NullString{}
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return false, err
		}

		if values[idIndex].Valid {
			id, err := strconv.ParseInt(values[idIndex].String, 10, 64)
			if err == nil && id == holderID {
				continue
			}
		}
		if values[infoIndex].Valid && strings.Contains(strings.ToUpper(values[infoIndex].String), "GET_LOCK") {
			return true, nil
		}
	}

	return false, rows.Err()
}
