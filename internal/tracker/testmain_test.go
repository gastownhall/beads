//go:build cgo

package tracker

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
)

// testServerPort is the port of the shared test Dolt server.
var testServerPort int

// testSharedDB is the name of the shared database for branch-per-test isolation.
var testSharedDB string

// testSharedConn is a raw *sql.DB for branch operations in the shared database.
var testSharedConn *sql.DB

// trackerDoltMu serializes a mid-suite server replacement. Tests in this
// package do not call t.Parallel, but a revive still swaps the port and the
// shared connection that every later newTestStore reads.
var trackerDoltMu sync.Mutex

// trackerDoltRevived bounds the replacement to one per process. A container
// that dies on every open would otherwise spend the job timeout restarting.
var trackerDoltRevived bool

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	os.Setenv("BEADS_TEST_MODE", "1")
	// AD-01 (be-c5p): allow tracker tests to connect to the test container.
	os.Setenv("BEADS_TEST_SERVER", "1")
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
		testServerPort = testutil.DoltContainerPortInt()

		// Set up shared database for branch-per-test isolation
		testSharedDB = "tracker_pkg_shared"
		db, err := testutil.SetupSharedTestDB(testServerPort, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed: %v\n", err)
			return 1
		}
		testSharedConn = db
		defer func() {
			if testSharedConn != nil {
				_ = testSharedConn.Close()
			}
		}()

		// Create schema + config on the shared DB and commit to main
		if err := initTrackerSharedSchema(testServerPort); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared schema init failed: %v\n", err)
			return 1
		}
	}

	code := m.Run()

	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	return code
}

func initTrackerSharedSchema(port int) error {
	ctx := context.Background()
	// A fresh directory each call. Reusing /tmp/tracker-shared-init after a
	// server replacement would keep the previous server's metadata and dial
	// the port that just died.
	dir, err := os.MkdirTemp("", "tracker-shared-init-")
	if err != nil {
		return fmt.Errorf("init dir: %w", err)
	}
	defer os.RemoveAll(dir)
	cfg := &dolt.Config{
		Path:         dir,
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

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		return fmt.Errorf("SetConfig(issue_prefix): %w", err)
	}

	// Commit schema to main so branches get a clean snapshot
	db := store.DB()
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("DOLT_ADD: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: init shared schema')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT: %w", err)
	}
	if err := testutil.MaterializeLocalTableSchemasForBranchTests(ctx, db); err != nil {
		return fmt.Errorf("materialize local table schemas: %w", err)
	}

	return nil
}

// reviveTrackerDolt replaces a shared container that has exited and rebuilds
// the branch-per-test database on it. One death used to fail every later
// TestEngine* with "Dolt server unreachable" (Main job "Test (storage domain
// + uow)", 2026-09-25, 27 failures after a single unexpected EOF).
func reviveTrackerDolt() error {
	trackerDoltMu.Lock()
	defer trackerDoltMu.Unlock()
	if trackerDoltRevived {
		if testutil.DoltContainerPortInt() != 0 && !testutil.DoltContainerCrashed() {
			testServerPort = testutil.DoltContainerPortInt()
			return nil
		}
		return fmt.Errorf("shared Dolt server died again after one replacement")
	}
	port, err := testutil.RestartSharedDoltContainer()
	if err != nil {
		return err
	}
	testServerPort = port
	if testSharedConn != nil {
		_ = testSharedConn.Close()
		testSharedConn = nil
	}
	db, err := testutil.SetupSharedTestDB(port, testSharedDB)
	if err != nil {
		return err
	}
	testSharedConn = db
	if err := initTrackerSharedSchema(port); err != nil {
		return err
	}
	trackerDoltRevived = true
	return nil
}
