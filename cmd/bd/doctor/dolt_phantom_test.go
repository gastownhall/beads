//go:build cgo

package doctor

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
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
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	// Ensure a "beads" database exists on the shared server for phantom tests.
	// This matches configfile.DefaultDoltDatabase which checkPhantomDatabases uses.
	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Timeout: 10 * time.Second}.String()
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

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: "beads", Timeout: 10 * time.Second}.String()
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

func TestCheckPhantomDatabases_GlobalDBNotPhantom(t *testing.T) {
	db := openSharedDoltForPhantom(t)

	// Clean up any pre-existing phantom databases from other tests. The shared
	// test container is bootstrapped with a beads_test database, so without this
	// sweep the aggregate-status assertion below is red under -run isolation,
	// -shuffle, or sharding, and green in a package run only because
	// TestCheckPhantomDatabases_OK happens to sweep first.
	cleanupAllPhantomDBs(t, db)

	// Shared-server mode creates a global routing database (beads_global) that
	// matches the beads_ prefix but is intentional, not a phantom.
	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_global")
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	cleanupPhantomDB(t, db, "beads_global")

	conn := &doltConn{
		db:  db,
		cfg: &configfile.Config{GlobalDoltDatabase: "beads_global"},
	}
	check := checkPhantomDatabases(conn)

	// Containment pins this test's own subject and cannot be perturbed by an
	// unrelated database appearing on the shared server; the status assertion
	// then adds that nothing else was flagged either.
	if strings.Contains(check.Message, "beads_global") {
		t.Errorf("global DB must not be reported as a phantom, got %s: %s", check.Status, check.Message)
	}
	if check.Status != StatusOK {
		t.Errorf("expected StatusOK (global DB should not be flagged), got %s: %s", check.Status, check.Message)
	}
}

// TestCheckPhantomDatabases_GlobalDBNoStamp covers the population that reported
// GH#6599: an existing checkout whose metadata.json carries no
// global_dolt_database stamp, because bd init only writes that field when the
// workspace is initialized under shared-server mode and nothing back-fills it.
func TestCheckPhantomDatabases_GlobalDBNoStamp(t *testing.T) {
	db := openSharedDoltForPhantom(t)
	cleanupAllPhantomDBs(t, db)

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_global")
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	cleanupPhantomDB(t, db, "beads_global")

	conn := &doltConn{db: db, cfg: &configfile.Config{}}
	check := checkPhantomDatabases(conn)

	if strings.Contains(check.Message, "beads_global") {
		t.Errorf("unstamped global DB must not be flagged in shared-server mode, got %s: %s", check.Status, check.Message)
	}
	if check.Status != StatusOK {
		t.Errorf("expected StatusOK for an unstamped global DB in shared-server mode, got %s: %s", check.Status, check.Message)
	}
}

// TestCheckPhantomDatabases_GlobalDBNoStampPerProject pins the other arm of the
// fallback: with neither the init stamp nor active shared-server mode there is
// no evidence this workspace routes through a global database, so a stray
// beads_global on a per-project server is still reported. This covers unstamped
// workspaces only — the stamp is read ahead of the mode gate, so a workspace
// stamped under shared-server mode keeps skipping beads_global after the mode is
// turned off. That is deliberate (bd init writes the same constant the fallback
// returns, so the two can only diverge on a hand-edited metadata.json).
func TestCheckPhantomDatabases_GlobalDBNoStampPerProject(t *testing.T) {
	db := openSharedDoltForPhantom(t)
	cleanupAllPhantomDBs(t, db)

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	if doltserver.IsSharedServerMode() {
		t.Skip("shared-server mode enabled via config.yaml; cannot exercise the per-project arm")
	}

	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_global")
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	cleanupPhantomDB(t, db, "beads_global")

	conn := &doltConn{db: db, cfg: &configfile.Config{}}
	check := checkPhantomDatabases(conn)

	if !strings.Contains(check.Message, "beads_global") {
		t.Errorf("expected beads_global to be flagged without a stamp outside shared-server mode, got %s: %s", check.Status, check.Message)
	}
}

// TestProbeForCorrectDatabase_SkipsGlobalDB covers the adjacent exit of the same
// fix. The global routing database is schema-initialized, so it answers the
// probe's issues-table query and would otherwise be suggested as the project's
// real database.
func TestProbeForCorrectDatabase_SkipsGlobalDB(t *testing.T) {
	db := openSharedDoltForPhantom(t)
	cleanupAllPhantomDBs(t, db)

	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_global")
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	cleanupPhantomDB(t, db, "beads_global")
	//nolint:gosec // G202: test-only database name, not user input
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS `beads_global`.issues (id VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("failed to create issues table: %v", err)
	}

	// The configured database ("beads") is skipped by the probe, so beads_global
	// is the first candidate it reaches.
	conn := &doltConn{
		db:  db,
		cfg: &configfile.Config{DoltDatabase: "beads", GlobalDoltDatabase: "beads_global"},
	}

	if got := probeForCorrectDatabase(conn); got == "beads_global" {
		t.Errorf("probeForCorrectDatabase returned the shared-server global database %q", got)
	}
}
