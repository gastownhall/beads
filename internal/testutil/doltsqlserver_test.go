package testutil

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// holdLoopbackPort binds a loopback port for the rest of the test, so a dolt
// sql-server told to use it loses the bind the way it does when another
// process wins the FindFreePort race.
func holdLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestStartDoltSQLServer_RetriesOnFreshPortAfterBindLoss(t *testing.T) {
	RequireDoltBinary(t)
	held := holdLoopbackPort(t)
	calls := 0
	pick := func() (int, error) {
		calls++
		if calls == 1 {
			return held, nil
		}
		return FindFreePort()
	}

	srv, err := startDoltSQLServer(t.TempDir(), pick, DoltSQLServerReadyTimeout)
	if err != nil {
		t.Fatalf("expected a retry on a fresh port to succeed, got: %v", err)
	}
	t.Cleanup(srv.stop)
	if srv.port == held {
		t.Fatalf("server reported the held port %d", held)
	}
	if calls != 2 {
		t.Fatalf("expected exactly one retry (2 port picks), got %d", calls)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.port)))
	if err != nil {
		t.Fatalf("server on port %d not reachable: %v", srv.port, err)
	}
	_ = conn.Close()
}

func TestStartDoltSQLServer_BoundedRetriesReportDoltOutput(t *testing.T) {
	RequireDoltBinary(t)
	held := holdLoopbackPort(t)
	calls := 0
	pick := func() (int, error) { calls++; return held, nil }

	_, err := startDoltSQLServer(t.TempDir(), pick, DoltSQLServerReadyTimeout)
	if err == nil {
		t.Fatal("expected failure when every attempt loses the bind")
	}
	if calls != doltSQLServerStartAttempts {
		t.Fatalf("expected %d attempts, got %d", doltSQLServerStartAttempts, calls)
	}
	if !errors.Is(err, errDoltSQLServerBind) {
		t.Fatalf("expected a bind error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("error should carry dolt's output and exit status, got: %v", err)
	}
}

func TestStartDoltSQLServer_NonBindExitIsNotRetried(t *testing.T) {
	RequireDoltBinary(t)
	// A regular file as --data-dir makes dolt exit at startup for a reason
	// other than the port.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	pick := func() (int, error) { calls++; return FindFreePort() }

	_, err := startDoltSQLServer(notADir, pick, DoltSQLServerReadyTimeout)
	if err == nil {
		t.Fatal("expected dolt sql-server to fail on a non-directory --data-dir")
	}
	if calls != 1 {
		t.Fatalf("a non-bind failure must not be retried, got %d attempts: %v", calls, err)
	}
	if errors.Is(err, errDoltSQLServerBind) {
		t.Fatalf("misclassified as a bind failure: %v", err)
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("expected the early exit to be reported with dolt's output, got: %v", err)
	}
}
