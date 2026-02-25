package doctor

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
)

// openTestDoltForDoctor starts a temporary dolt sql-server and returns a
// connection. Adapted from migrations_test.go openTestDolt, but creates only
// a "beads" database (matching DefaultDoltDatabase) and uses a separate port
// range to avoid collisions with concurrent migration tests.
func openTestDoltForDoctor(t *testing.T) *sql.DB {
	t.Helper()

	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt binary not found, skipping phantom test")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "testdb")
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		t.Fatalf("failed to create db dir: %v", err)
	}

	// Filter DOLT_ROOT_PASSWORD to prevent auth interference in Doppler environments.
	var filteredEnv []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "DOLT_ROOT_PASSWORD=") {
			continue
		}
		filteredEnv = append(filteredEnv, e)
	}
	doltEnv := append(filteredEnv, "DOLT_ROOT_PATH="+tmpDir)

	for _, cfg := range []struct{ key, val string }{
		{"user.name", "Test User"},
		{"user.email", "test@example.com"},
	} {
		cfgCmd := exec.Command("dolt", "config", "--global", "--add", cfg.key, cfg.val)
		cfgCmd.Env = doltEnv
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt config %s failed: %v\n%s", cfg.key, err, out)
		}
	}

	// Initialize dolt repo
	initCmd := exec.Command("dolt", "init")
	initCmd.Dir = dbPath
	initCmd.Env = doltEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %v\n%s", err, out)
	}

	// Create beads database (matching DefaultDoltDatabase)
	sqlCmd := exec.Command("dolt", "sql", "-q", "CREATE DATABASE IF NOT EXISTS beads")
	sqlCmd.Dir = dbPath
	sqlCmd.Env = doltEnv
	if out, err := sqlCmd.CombinedOutput(); err != nil {
		t.Fatalf("create database failed: %v\n%s", err, out)
	}

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	// Start dolt sql-server
	serverCmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
	)
	serverCmd.Dir = dbPath
	serverCmd.Env = doltEnv
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})

	// Wait for server to be ready
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/beads?allowCleartextPasswords=true&allowNativePasswords=true", port)
	var db *sql.DB
	var lastPingErr error
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		db, err = sql.Open("mysql", dsn)
		if err != nil {
			continue
		}
		if pingErr := db.Ping(); pingErr == nil {
			lastPingErr = nil
			break
		} else {
			lastPingErr = pingErr
		}
		_ = db.Close()
		db = nil
	}
	if db == nil {
		t.Fatalf("dolt server not ready after retries: %v", lastPingErr)
	}
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func TestCheckPhantomDatabases_Warning(t *testing.T) {
	db := openTestDoltForDoctor(t)

	// Create a phantom database with beads_ prefix
	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_phantom")
	if err != nil {
		t.Fatalf("failed to create phantom database: %v", err)
	}

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
	db := openTestDoltForDoctor(t)

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
	db := openTestDoltForDoctor(t)

	// Create a phantom database with _beads suffix
	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS acf_beads")
	if err != nil {
		t.Fatalf("failed to create phantom database: %v", err)
	}

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
	db := openTestDoltForDoctor(t)

	// Create a database that matches beads_ prefix but IS the configured database
	//nolint:gosec // G202: test-only database name, not user input
	_, err := db.Exec("CREATE DATABASE IF NOT EXISTS beads_test")
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}

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
	db := openTestDoltForDoctor(t)

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
