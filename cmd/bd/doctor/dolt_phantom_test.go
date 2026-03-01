//go:build cgo

package doctor

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
)

// openSharedDoltForPhantom returns a *sql.DB connected to a "beads" database on
// the shared test server. Phantom tests need server-level CREATE/DROP DATABASE,
// so they use the shared server but operate at the database level rather than
// using branch isolation.
func openSharedDoltForPhantom(t *testing.T) *sql.DB {
	t.Helper()

	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping phantom test")
	}
	if testServer.IsCrashed() {
		t.Skipf("Dolt test server crashed: %v", testServer.CrashError())
	}

	// Ensure a "beads" database exists on the shared server for phantom tests.
	// This matches configfile.DefaultDoltDatabase which checkPhantomDatabases uses.
	rootDSN := fmt.Sprintf("root@tcp(127.0.0.1:%d)/?parseTime=true&timeout=10s", port)
	rootDB, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("failed to open root connection: %v", err)
	}
	_, err = rootDB.Exec("CREATE DATABASE IF NOT EXISTS beads")
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if !strings.Contains(errLower, "database exists") && !strings.Contains(errLower, "1007") {
			rootDB.Close()
			t.Fatalf("failed to create beads database: %v", err)
		}
	}
	rootDB.Close()

	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/beads?parseTime=true&timeout=10s", port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("failed to open connection: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}

// cleanupAllPhantomDBs removes any pre-existing phantom databases that might
// have been left by other tests sharing the same Dolt server.
func cleanupAllPhantomDBs(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		t.Fatalf("failed to list databases for cleanup: %v", err)
	}
	defer rows.Close()

	var phantoms []string
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			continue
		}
		if dbName == "information_schema" || dbName == "mysql" || dbName == "beads" {
			continue
		}
		if strings.HasPrefix(dbName, "beads_") || strings.HasSuffix(dbName, "_beads") {
			phantoms = append(phantoms, dbName)
		}
	}

	for _, name := range phantoms {
		//nolint:gosec // G202: test-only database name from SHOW DATABASES, not user input
		_, _ = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
	}
}

// cleanupPhantomDB drops a test phantom database (best-effort cleanup).
func cleanupPhantomDB(t *testing.T, db *sql.DB, dbName string) {
	t.Helper()
	t.Cleanup(func() {
		//nolint:gosec // G202: test-only database name, not user input
		_, _ = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	})
}

func TestCheckPhantomDatabases_Warning(t *testing.T) {
	db := openSharedDoltForPhantom(t)

	// Create a phantom database with beads_ prefix
	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_phantom")
	if err != nil {
		t.Fatalf("failed to create phantom database: %v", err)
	}
	cleanupPhantomDB(t, db, "beads_phantom")

	conn := &doltConn{db: db, cfg: nil}
	check := checkPhantomDatabases(conn)

	if check.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %s: %s", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "beads_phantom") {
		t.Errorf("expected message to contain 'beads_phantom', got: %s", check.Message)
	}
	if check.Category != CategoryData {
		t.Errorf("expected CategoryData, got %q", check.Category)
	}
	if !strings.Contains(check.Fix, "GH#2051") {
		t.Errorf("expected fix to reference GH#2051, got: %s", check.Fix)
	}
}

func TestCheckPhantomDatabases_OK(t *testing.T) {
	db := openSharedDoltForPhantom(t)

	// Clean up any pre-existing phantom databases from other tests
	cleanupAllPhantomDBs(t, db)

	// No phantom databases — only system DBs and "beads" (the configured default)
	conn := &doltConn{db: db, cfg: nil}
	check := checkPhantomDatabases(conn)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK, got %s: %s", check.Status, check.Message)
	}
	if check.Name != "Phantom Databases" {
		t.Errorf("expected check name 'Phantom Databases', got %q", check.Name)
	}
}

func TestCheckPhantomDatabases_SuffixPattern(t *testing.T) {
	db := openSharedDoltForPhantom(t)

	// Create a phantom database with _beads suffix
	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS acf_beads")
	if err != nil {
		t.Fatalf("failed to create phantom database: %v", err)
	}
	cleanupPhantomDB(t, db, "acf_beads")

	conn := &doltConn{db: db, cfg: nil}
	check := checkPhantomDatabases(conn)

	if check.Status != StatusWarning {
		t.Errorf("expected StatusWarning for _beads suffix, got %s: %s", check.Status, check.Message)
	}
	if !strings.Contains(check.Message, "acf_beads") {
		t.Errorf("expected message to contain 'acf_beads', got: %s", check.Message)
	}
}

func TestCheckPhantomDatabases_ConfiguredDBNotPhantom(t *testing.T) {
	db := openSharedDoltForPhantom(t)

	// Create a database that matches beads_ prefix but IS the configured database
	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_test")
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	cleanupPhantomDB(t, db, "beads_test")

	// Configure the connection so beads_test IS the configured database
	conn := &doltConn{
		db:  db,
		cfg: &configfile.Config{DoltDatabase: "beads_test"},
	}
	check := checkPhantomDatabases(conn)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK (configured DB should not be flagged), got %s: %s", check.Status, check.Message)
	}
}

func TestCheckPhantomDatabases_NilConfig(t *testing.T) {
	db := openSharedDoltForPhantom(t)

	// Clean up any pre-existing phantom databases from other tests
	cleanupAllPhantomDBs(t, db)

	// With nil config, should use DefaultDoltDatabase ("beads") as the configured name.
	// "beads" has no beads_ prefix or _beads suffix matching issues, so it's safe.
	// No phantom databases present — should be OK.
	conn := &doltConn{db: db, cfg: nil}
	check := checkPhantomDatabases(conn)

	if check.Status != StatusOK {
		t.Errorf("expected StatusOK with nil config and no phantoms, got %s: %s", check.Status, check.Message)
	}

	// Verify the function doesn't panic or error with nil config
	if check.Name != "Phantom Databases" {
		t.Errorf("expected check name 'Phantom Databases', got %q", check.Name)
	}
}
