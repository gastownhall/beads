package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// testServerPort is the port of the shared test Dolt server (0 = not running).
// Set by TestMain before tests run, used implicitly via BEADS_DOLT_PORT env var
// which applyConfigDefaults reads when ServerPort is 0.
var testServerPort int

// testSharedDB is the name of the shared database for branch-per-test isolation.
var testSharedDB string

// testSharedConn is a raw *sql.DB for branch operations in the shared database.
var testSharedConn *sql.DB

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	srv, cleanup := testutil.StartTestDoltServer("dolt-pkg-test-*")
	defer cleanup()

	os.Setenv("BEADS_TEST_MODE", "1")
	if srv != nil {
		testServerPort = srv.Port
		os.Setenv("BEADS_DOLT_PORT", fmt.Sprintf("%d", srv.Port))

		// Set up shared database for branch-per-test isolation
		testSharedDB = "dolt_pkg_shared"
		db, err := testutil.SetupSharedTestDB(srv.Port, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed: %v\n", err)
			return 1
		}
		testSharedConn = db
		defer db.Close()

		// Create the schema by opening a store against the shared DB,
		// configuring it, and committing.
		if err := initSharedSchema(srv.Port); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared schema init failed: %v\n", err)
			return 1
		}
	}

	code := m.Run()

	testServerPort = 0
	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	return code
}

// initSharedSchema creates a store on the shared DB, sets config, and commits
// so that branches inherit the full schema.
func initSharedSchema(port int) error {
	ctx := context.Background()
	cfg := &Config{
		Path:         "/tmp/dolt-shared-init", // not used, just needs to be non-empty
		ServerHost:   "127.0.0.1",
		ServerPort:   port,
		Database:     testSharedDB,
		MaxOpenConns: 1,
	}
	store, err := New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("New: %w", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		return fmt.Errorf("SetConfig(issue_prefix): %w", err)
	}

	// Commit schema to main so branches get a clean snapshot
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("DOLT_ADD: %w", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: init shared schema')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT: %w", err)
	}

	return nil
}
