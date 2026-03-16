//go:build cgo

package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestStandaloneDoctorChecks_PreserveRedirectSourceDatabase(t *testing.T) {
	repoDir, cleanup := setupRedirectedStandaloneDoctorRepo(t)
	defer cleanup()

	connCheck := CheckDoltConnection(repoDir)
	if connCheck.Status != StatusOK {
		t.Fatalf("CheckDoltConnection status = %q, want %q (message: %s, detail: %s)", connCheck.Status, StatusOK, connCheck.Message, connCheck.Detail)
	}

	schemaCheck := CheckDoltSchema(repoDir)
	if schemaCheck.Status != StatusOK {
		t.Fatalf("CheckDoltSchema status = %q, want %q (message: %s, detail: %s)", schemaCheck.Status, StatusOK, schemaCheck.Message, schemaCheck.Detail)
	}

	issueCountCheck := CheckDoltIssueCount(repoDir)
	if issueCountCheck.Status != StatusOK {
		t.Fatalf("CheckDoltIssueCount status = %q, want %q (message: %s, detail: %s)", issueCountCheck.Status, StatusOK, issueCountCheck.Message, issueCountCheck.Detail)
	}
	if !strings.Contains(issueCountCheck.Message, "1 issues") {
		t.Fatalf("CheckDoltIssueCount message = %q, want it to contain %q", issueCountCheck.Message, "1 issues")
	}
}

func setupRedirectedStandaloneDoctorRepo(t *testing.T) (string, func()) {
	t.Helper()

	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}

	serverDataDir := t.TempDir()
	serverCmd := exec.Command("dolt", "sql-server", "--data-dir", serverDataDir, "-H", "127.0.0.1", "-P", fmt.Sprintf("%d", port))
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start dolt sql-server: %v", err)
	}
	cleanup := func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	}
	if !testutil.WaitForServer(port, 15*time.Second) {
		cleanup()
		t.Fatal("dolt sql-server did not become ready within timeout")
	}

	rootDB := openStandaloneDoctorTestDB(t, port, "")
	if _, err := rootDB.Exec("CREATE DATABASE source_db"); err != nil {
		cleanup()
		t.Fatalf("create source_db: %v", err)
	}
	_ = rootDB.Close()

	sourceDB := openStandaloneDoctorTestDB(t, port, "source_db")
	for _, stmt := range []string{
		"CREATE TABLE issues (id INT PRIMARY KEY)",
		"CREATE TABLE dependencies (id INT PRIMARY KEY)",
		"CREATE TABLE config (id INT PRIMARY KEY)",
		"CREATE TABLE labels (id INT PRIMARY KEY)",
		"CREATE TABLE events (id INT PRIMARY KEY)",
		"CREATE TABLE wisps (id INT PRIMARY KEY)",
		"CREATE TABLE wisp_labels (id INT PRIMARY KEY)",
		"CREATE TABLE wisp_dependencies (id INT PRIMARY KEY)",
		"CREATE TABLE wisp_events (id INT PRIMARY KEY)",
		"CREATE TABLE wisp_comments (id INT PRIMARY KEY)",
		"INSERT INTO issues VALUES (1)",
	} {
		if _, err := sourceDB.Exec(stmt); err != nil {
			_ = sourceDB.Close()
			cleanup()
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	_ = sourceDB.Close()

	repoDir := t.TempDir()
	sourceBeadsDir := filepath.Join(repoDir, ".beads")
	targetRoot := t.TempDir()
	targetBeadsDir := filepath.Join(targetRoot, ".beads")
	if err := os.MkdirAll(sourceBeadsDir, 0o755); err != nil {
		cleanup()
		t.Fatalf("mkdir source beads dir: %v", err)
	}
	if err := os.MkdirAll(targetBeadsDir, 0o755); err != nil {
		cleanup()
		t.Fatalf("mkdir target beads dir: %v", err)
	}

	sourceCfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "source_db",
	}
	if err := sourceCfg.Save(sourceBeadsDir); err != nil {
		cleanup()
		t.Fatalf("save source config: %v", err)
	}

	targetCfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltServerUser: "root",
		DoltDatabase:   "target_db",
		ProjectID:      "standalone-doctor-test",
	}
	if err := targetCfg.Save(targetBeadsDir); err != nil {
		cleanup()
		t.Fatalf("save target config: %v", err)
	}

	if err := os.WriteFile(filepath.Join(sourceBeadsDir, beads.RedirectFileName), []byte(targetBeadsDir+"\n"), 0o644); err != nil {
		cleanup()
		t.Fatalf("write redirect: %v", err)
	}

	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)

	return repoDir, cleanup
}

func openStandaloneDoctorTestDB(t *testing.T, port int, database string) *sql.DB {
	t.Helper()

	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s?parseTime=true&timeout=10s", port, database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", database, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("db.Ping(%q): %v", database, err)
	}
	return db
}
