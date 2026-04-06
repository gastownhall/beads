package migrations

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
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
	os.Setenv("BEADS_TEST_MODE", "1")
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
		testServerPort = testutil.DoltContainerPortInt()

		// Set up shared database for branch-per-test isolation.
		// The base schema (issues table) is committed to main so that
		// branches inherit it via COW snapshots.
		testSharedDB = "migrations_pkg_shared"
		db, err := testutil.SetupSharedTestDB(testServerPort, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed: %v\n", err)
			return 1
		}
		testSharedConn = db
		defer db.Close()

		// Create minimal issues table and commit to main.
		// Migration tests start from this base schema.
		if err := initMigrationSharedSchema(testServerPort); err != nil {
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

// initMigrationSharedSchema creates the minimal issues table and commits it
// to main so branches get a clean snapshot.
func initMigrationSharedSchema(port int) error {
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: testSharedDB, Timeout: 10 * time.Second}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open connection: %w", err)
	}
	defer db.Close()

	// Create the minimal issues table (simulating old schema for migration tests)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS issues (
		id VARCHAR(255) PRIMARY KEY,
		title VARCHAR(500) NOT NULL,
		status VARCHAR(32) NOT NULL DEFAULT 'open',
		ephemeral TINYINT(1) DEFAULT 0,
		pinned TINYINT(1) DEFAULT 0
	)`)
	if err != nil {
		return fmt.Errorf("create issues table: %w", err)
	}

	// Commit schema to main so branches inherit it
	if _, err := db.Exec("CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("DOLT_ADD: %w", err)
	}
	if _, err := db.Exec("CALL DOLT_COMMIT('--allow-empty', '-m', 'test: init migrations shared schema')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT: %w", err)
	}

	return nil
}
