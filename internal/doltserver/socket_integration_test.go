//go:build integration && !windows

package doltserver_test

import (
	"database/sql"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestUnixSocket_ConnectAndQuery starts a Dolt server with --socket,
// connects via the unix socket DSN, and executes SQL to verify end-to-end
// unix socket connectivity. Covers the full path from GH#2939.
func TestUnixSocket_ConnectAndQuery(t *testing.T) {
	doltBin := integration.RequireDolt(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Initialize dolt database.
	initCmd := exec.Command(doltBin, "init")
	initCmd.Dir = doltDir
	initCmd.Env = append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init: %v\n%s", err, out)
	}

	socketPath := filepath.Join(tmpDir, "dolt.sock")

	// Start dolt sql-server with --socket and no TCP listener.
	serverCmd := exec.Command(doltBin, "sql-server",
		"--socket", socketPath,
		"--loglevel=warning",
		"-P", "0",
	)
	serverCmd.Dir = doltDir
	serverCmd.Env = append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
	serverCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logFile := filepath.Join(tmpDir, "dolt-server.log")
	lf, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	serverCmd.Stdout = lf
	serverCmd.Stderr = lf

	if err := serverCmd.Start(); err != nil {
		lf.Close()
		t.Fatalf("dolt sql-server start: %v", err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Signal(syscall.SIGTERM)
		_ = serverCmd.Wait()
		lf.Close()
	})

	// Wait for the socket file to become connectable.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Connect via unix socket DSN.
	dsn := doltutil.ServerDSN{
		Socket: socketPath,
		User:   "root",
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// Verify connectivity.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.Ping(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := db.Ping(); err != nil {
		logs, _ := os.ReadFile(logFile)
		t.Fatalf("db.Ping via socket failed: %v\nServer logs:\n%s", err, logs)
	}

	// Create, insert, query — prove the socket carries real MySQL traffic.
	if _, err := db.Exec("CREATE TABLE socket_test (id INT PRIMARY KEY, val VARCHAR(100))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO socket_test VALUES (1, 'socket-works')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var val string
	if err := db.QueryRow("SELECT val FROM socket_test WHERE id = 1").Scan(&val); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if val != "socket-works" {
		t.Errorf("expected 'socket-works', got %q", val)
	}
}
