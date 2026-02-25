package molecules

import (
	"context"
	"database/sql"
	"fmt"
	"os"
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

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	srv, cleanup := testutil.StartTestDoltServer("molecules-pkg-test-*")
	defer cleanup()

	os.Setenv("BEADS_TEST_MODE", "1")
	if srv != nil {
		testServerPort = srv.Port
		os.Setenv("BEADS_DOLT_PORT", fmt.Sprintf("%d", srv.Port))

		// Set up shared database for branch-per-test isolation
		testSharedDB = "molecules_pkg_shared"
		db, err := testutil.SetupSharedTestDB(srv.Port, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed: %v\n", err)
			return 1
		}
		testSharedConn = db
		defer db.Close()

		if err := initMoleculesSharedSchema(srv.Port); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared schema init failed: %v\n", err)
			return 1
		}
	}

	code := m.Run()

	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	return code
}

func initMoleculesSharedSchema(port int) error {
	ctx := context.Background()
	cfg := &dolt.Config{
		Path:         "/tmp/molecules-shared-init",
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
	if err := store.SetConfig(ctx, "types.custom", "molecule"); err != nil {
		return fmt.Errorf("SetConfig(types.custom): %w", err)
	}

	db := store.DB()
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("DOLT_ADD: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: init shared schema')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT: %w", err)
	}

	return nil
}
