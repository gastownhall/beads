//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
)

func init() {
	beforeTestsHook = startTestDoltServer
}

// testSharedDB is the name of the shared database for branch-per-test isolation.
var testSharedDB string

// testSharedConn is a raw *sql.DB for branch operations in the shared database.
var testSharedConn *sql.DB

// startTestDoltServer starts a dedicated Dolt SQL server in a container
// on a dynamic port using the shared testutil helper. This prevents tests
// from creating testdb_* databases on the production Dolt server.
// Returns a cleanup function that stops the server and removes the container.
func startTestDoltServer() func() {
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
		return func() {}
	}

	testDoltServerPort = testutil.DoltContainerPortInt()
	sharedInitRoot, err := os.MkdirTemp("", "beads-cmdbd-shared-init-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: shared init temp dir failed: %v (falling back to per-test DBs)\n", err)
	}

	// Set up shared database for branch-per-test isolation (bd-xmf).
	// Instead of CREATE/DROP DATABASE per test, tests branch from this
	// shared DB, eliminating ~1-2s of overhead per test.
	testSharedDB = fmt.Sprintf("cmdbd_pkg_shared_%d", os.Getpid())
	db, err := testutil.SetupSharedTestDB(testDoltServerPort, testSharedDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: shared DB setup failed: %v (falling back to per-test DBs)\n", err)
		testSharedDB = ""
	} else {
		testSharedConn = db
		if sharedInitRoot == "" {
			fmt.Fprintf(os.Stderr, "WARNING: shared schema init skipped: no isolated temp dir (falling back to per-test DBs)\n")
			testSharedDB = ""
			db.Close()
			testSharedConn = nil
		} else if err := initCmdBDSharedSchema(sharedInitRoot, testDoltServerPort); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: shared schema init failed: %v (falling back to per-test DBs)\n", err)
			testSharedDB = ""
			db.Close()
			testSharedConn = nil
		}
	}
	if testDoltServerPort != 0 {
		if err := testutil.WaitForSQLServer(testDoltServerPort, 30*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: shared Dolt server not stable after cmd/bd harness setup: %v (falling back to per-test DBs)\n", err)
			if testSharedConn != nil {
				testSharedConn.Close()
				testSharedConn = nil
			}
			testSharedDB = ""
		}
	}

	return func() {
		if testSharedConn != nil {
			testSharedConn.Close()
			testSharedConn = nil
		}
		testSharedDB = ""
		testDoltServerPort = 0
		os.Unsetenv("BEADS_DOLT_PORT")
		if sharedInitRoot != "" {
			_ = os.RemoveAll(sharedInitRoot)
		}
		testutil.TerminateDoltContainer()
	}
}

// initCmdBDSharedSchema initializes the schema and config on the shared database
// and commits to main so branches get a clean snapshot.
func initCmdBDSharedSchema(sharedInitRoot string, port int) error {
	ctx := context.Background()
	beadsDir := filepath.Join(sharedInitRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir shared init beads dir: %w", err)
	}
	cfg := &dolt.Config{
		Path:         filepath.Join(beadsDir, "dolt"),
		BeadsDir:     beadsDir,
		ServerHost:   "127.0.0.1",
		ServerPort:   port,
		Database:     testSharedDB,
		MaxOpenConns: 1,
	}
	store, err := dolt.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("New: %w", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		return fmt.Errorf("SetConfig(issue_prefix): %w", err)
	}
	if err := store.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		return fmt.Errorf("SetConfig(types.custom): %w", err)
	}

	// Commit schema to main so branches get a clean snapshot
	db := store.DB()
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("DOLT_ADD: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: init shared schema')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT: %w", err)
	}

	return nil
}
