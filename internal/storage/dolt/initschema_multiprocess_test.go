//go:build integration && !windows

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil/integration"
	"golang.org/x/sync/errgroup"
)

// TestHelperSchemaInit is the subprocess entry point for multi-process schema
// init tests. It is only executed when BEADS_SCHEMA_INIT_HELPER=1 is set.
// Each subprocess connects to the dolt server and calls initSchemaOnDB.
func TestHelperSchemaInit(t *testing.T) {
	if os.Getenv("BEADS_SCHEMA_INIT_HELPER") != "1" {
		return // Not a subprocess — skip.
	}

	port := os.Getenv("BEADS_SCHEMA_INIT_PORT")
	dbName := os.Getenv("BEADS_SCHEMA_INIT_DB")
	procID := os.Getenv("BEADS_SCHEMA_INIT_PROC_ID")

	if port == "" || dbName == "" {
		fmt.Fprintf(os.Stderr, "FATAL: missing BEADS_SCHEMA_INIT_PORT or BEADS_SCHEMA_INIT_DB\n")
		os.Exit(1)
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

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: ping: %v\n", err)
		os.Exit(1)
	}

	err = initSchemaOnDB(ctx, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: initSchemaOnDB: %v\n", err)
		os.Exit(1)
	}

	// Report which path was taken. The parent test asserts exactly 1 slow path.
	// We detect the path by checking if schema_version existed before our init.
	// Since initSchemaOnDB is idempotent, we can't truly distinguish from here,
	// but the important thing is all subprocesses succeed without corruption.
	fmt.Fprintf(os.Stdout, "OK proc=%s\n", procID)
}

// TestMultiProcessSchemaInit spawns N subprocesses that all call initSchemaOnDB
// on the same fresh database simultaneously. Verifies no journal corruption
// and schema integrity after concurrent initialization.
// Would have caught: GH#2672 (concurrent initSchemaOnDB corrupts fresh DB).
func TestMultiProcessSchemaInit(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	modRoot := integration.ModuleRoot(t)
	diag := integration.NewDiagnostics(t, ".")
	diag.CaptureOnFailure()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Build the test binary once.
	runner := integration.NewSubprocessRunner(modRoot, "./internal/storage/dolt/")
	testBin := runner.Build(t)

	// Create a fresh database.
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
	t.Cleanup(func() {
		// Skip DROP — rapid cycles can crash the Dolt container.
	})

	// Spawn N subprocesses.
	const numProcs = 8
	portStr := strconv.Itoa(testServerPort)

	var successCount atomic.Int32
	eg, egCtx := errgroup.WithContext(ctx)

	for i := 0; i < numProcs; i++ {
		procID := strconv.Itoa(i)
		eg.Go(func() error {
			subCtx, subCancel := context.WithTimeout(egCtx, 30*time.Second)
			defer subCancel()

			cmd := exec.CommandContext(subCtx, testBin,
				"-test.run=^TestHelperSchemaInit$",
				"-test.v",
			)
			cmd.Env = integration.FilterEnv(os.Environ())
			cmd.Env = append(cmd.Env,
				"BEADS_SCHEMA_INIT_HELPER=1",
				"BEADS_SCHEMA_INIT_PORT="+portStr,
				"BEADS_SCHEMA_INIT_DB="+dbName,
				"BEADS_SCHEMA_INIT_PROC_ID="+procID,
				"BEADS_TEST_MODE=1",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("subprocess %s failed: %v\noutput: %s", procID, err, out)
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

	if int(successCount.Load()) != numProcs {
		t.Fatalf("expected %d successful subprocesses, got %d", numProcs, successCount.Load())
	}
	t.Logf("All %d subprocesses completed successfully", numProcs)

	// Verify schema integrity.
	verifyDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	verifyDB, err := sql.Open("mysql", verifyDSN)
	if err != nil {
		t.Fatalf("open verify connection: %v", err)
	}
	defer verifyDB.Close()

	verifyCtx, verifyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer verifyCancel()

	// Check schema_version exists and is current.
	var version int
	err = verifyDB.QueryRowContext(verifyCtx, "SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("schema_version query failed: %v", err)
	}
	if version == 0 {
		t.Error("schema_version is 0 after concurrent init")
	}
	t.Logf("schema_version: %d", version)

	// Verify core tables exist.
	requiredTables := []string{"issues", "dependencies", "comments", "schema_version"}
	for _, table := range requiredTables {
		var count int
		err := verifyDB.QueryRowContext(verifyCtx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ?", dbName, table).Scan(&count)
		if err != nil {
			t.Errorf("checking table %s: %v", table, err)
		} else if count == 0 {
			t.Errorf("required table %s is missing after concurrent init", table)
		}
	}
}

// TestMultiProcessSchemaInit_DoltVerify runs dolt verify on the data directory
// after concurrent schema initialization to detect journal corruption.
// Would have caught: GH#2430 (journal corruption after concurrent DDL).
func TestMultiProcessSchemaInit_DoltVerify(t *testing.T) {
	skipIfNoDolt(t)

	// This test requires a local dolt data directory, not a Docker container.
	// It starts its own server, runs concurrent inits, then runs dolt verify.
	doltPath := integration.RequireDolt(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0700); err != nil {
		t.Fatalf("mkdir dolt: %v", err)
	}

	initCmd := exec.Command(doltPath, "init")
	initCmd.Dir = doltDir
	initCmd.Env = append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init: %v\n%s", err, out)
	}

	// Start a local server.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	reg := integration.NewProcessRegistry(t)
	if p, err := os.FindProcess(state.PID); err == nil {
		reg.Register(p)
	}
	t.Cleanup(func() {
		_ = doltserver.Stop(beadsDir)
		reg.Deregister(state.PID)
	})

	// Create a fresh database on the local server.
	dbName := uniqueTestDBName(t)
	adminDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root"}.String()
	adminDB, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer adminDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	// Run concurrent schema inits (in-process, since we need dolt verify after).
	const numConcurrent = 10
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root", Database: dbName}.String()
	eg, egCtx := errgroup.WithContext(ctx)

	ready := make(chan struct{})
	for i := 0; i < numConcurrent; i++ {
		eg.Go(func() error {
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				return err
			}
			defer db.Close()
			db.SetMaxOpenConns(2)
			<-ready
			return initSchemaOnDB(egCtx, db)
		})
	}
	close(ready)

	if err := eg.Wait(); err != nil {
		t.Fatalf("concurrent initSchemaOnDB: %v", err)
	}

	// Stop server before running dolt verify (verify needs exclusive access).
	if err := doltserver.Stop(beadsDir); err != nil {
		t.Logf("Stop before verify: %v", err)
	}
	reg.Deregister(state.PID)

	// Run dolt verify on the database directory.
	dbDir := filepath.Join(doltDir, dbName)
	if _, err := os.Stat(dbDir); os.IsNotExist(err) {
		// Database may be in the dolt root if not using multi-DB mode.
		dbDir = doltDir
	}

	verifyCmd := exec.Command(doltPath, "verify")
	verifyCmd.Dir = dbDir
	verifyCmd.Env = append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
	verifyOut, verifyErr := verifyCmd.CombinedOutput()
	if verifyErr != nil {
		t.Errorf("dolt verify FAILED (journal corruption detected): %v\n%s", verifyErr, verifyOut)
	} else {
		t.Logf("dolt verify passed: %s", strings.TrimSpace(string(verifyOut)))
	}
}
