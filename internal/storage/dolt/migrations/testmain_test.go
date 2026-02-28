package migrations

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/testutil"
)

// testServerPort is the port of the shared test Dolt server (0 = not running).
var testServerPort int

// testSharedDB is the name of the shared database for branch-per-test isolation.
var testSharedDB string

// testSharedConn is a raw *sql.DB for branch operations in the shared database.
var testSharedConn *sql.DB

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	srv, cleanup := testutil.StartTestDoltServer("migrations-test-*")
	defer cleanup()

	os.Setenv("BEADS_TEST_MODE", "1")
	if srv != nil {
		testServerPort = srv.Port
		os.Setenv("BEADS_DOLT_PORT", fmt.Sprintf("%d", srv.Port))

		// Set up shared database for branch-per-test isolation.
		// No schema is committed — each test creates its own tables on its branch.
		testSharedDB = "migrations_pkg_shared"
		db, err := testutil.SetupSharedTestDB(srv.Port, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed: %v\n", err)
			return 1
		}
		testSharedConn = db
		defer db.Close()

		// Commit empty state to main so DOLT_BRANCH can create branches.
		if err := initMigrationsSharedSchema(srv.Port); err != nil {
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

func initMigrationsSharedSchema(port int) error {
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s?parseTime=true&timeout=10s", port, testSharedDB)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Commit an initial empty state so DOLT_BRANCH can create branches.
	if _, err := db.Exec("CALL DOLT_COMMIT('--allow-empty', '-m', 'init: empty schema for migration tests')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT: %w", err)
	}
	return nil
}
