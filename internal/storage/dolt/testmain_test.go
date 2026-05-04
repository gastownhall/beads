package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
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
	os.Setenv("BEADS_TEST_MODE", "1")

	// Escape hatch for long-running benches: when
	// BEADS_TEST_EXTERNAL_DOLT_PORT is set, skip the shared testcontainer
	// and assume an already-running `dolt sql-server` is reachable on that
	// port. Docker testcontainers get saturated at 10K-scale write benches
	// (be-nu4.4.1 / be-s54: i/o timeouts after ~3 samples of seeding 10K
	// rows + 30 DOLT_COMMITs). A persistent subprocess with full host
	// resources handles the sustained load that an isolated container
	// cannot. See docs/plans/be-eei-d4v2-composite-build.md for the
	// justification mail thread.
	if extPortStr := os.Getenv("BEADS_TEST_EXTERNAL_DOLT_PORT"); extPortStr != "" {
		port, err := strconv.Atoi(extPortStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: invalid BEADS_TEST_EXTERNAL_DOLT_PORT %q: %v\n", extPortStr, err)
			return 1
		}
		testServerPort = port
		if err := os.Setenv("BEADS_DOLT_PORT", extPortStr); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: set BEADS_DOLT_PORT: %v\n", err)
			return 1
		}
		testSharedDB = "dolt_pkg_shared"
		db, err := testutil.SetupSharedTestDB(testServerPort, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed (external dolt on port %d): %v\n", port, err)
			return 1
		}
		testSharedConn = db
		defer db.Close()

		if err := initSharedSchema(testServerPort); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared schema init failed: %v\n", err)
			return 1
		}
	} else if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
		testServerPort = testutil.DoltContainerPortInt()

		// Set up shared database for branch-per-test isolation
		testSharedDB = "dolt_pkg_shared"
		db, err := testutil.SetupSharedTestDB(testServerPort, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed: %v\n", err)
			return 1
		}
		testSharedConn = db
		defer db.Close()

		// Create the schema by opening a store against the shared DB,
		// configuring it, and committing.
		if err := initSharedSchema(testServerPort); err != nil {
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
		Path:            "/tmp/dolt-shared-init", // not used, just needs to be non-empty
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        testSharedDB,
		MaxOpenConns:    1,
		CreateIfMissing: true, // TestMain creates the shared database
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
