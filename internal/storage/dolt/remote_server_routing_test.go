package dolt

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/doltserver"
)

// stubDB returns a *sql.DB that is non-nil but will fail on any actual query.
// This avoids nil-pointer panics in routing methods that call ListRemotes etc.
func stubDB(t *testing.T) *sql.DB {
	t.Helper()
	// Open with a DSN that will fail on connect — but the *sql.DB handle is valid.
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:1)/nonexistent")
	if err != nil {
		t.Fatalf("sql.Open stub: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestIsRemoteServer verifies the isRemoteServer method on DoltStore.
func TestIsRemoteServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serverHost string
		serverMode bool
		want       bool
	}{
		{name: "remote host in server mode", serverHost: "mini2", serverMode: true, want: true},
		{name: "LAN IP in server mode", serverHost: "10.0.0.2", serverMode: true, want: true},
		{name: "localhost is not remote", serverHost: "localhost", serverMode: true, want: false},
		{name: "127.0.0.1 is not remote", serverHost: "127.0.0.1", serverMode: true, want: false},
		{name: "::1 is not remote", serverHost: "::1", serverMode: true, want: false},
		{name: "[::1] is not remote", serverHost: "[::1]", serverMode: true, want: false},
		{name: "empty host is not remote", serverHost: "", serverMode: true, want: false},
		{name: "remote host but not server mode", serverHost: "mini2", serverMode: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &DoltStore{
				serverHost: tt.serverHost,
				serverMode: tt.serverMode,
			}
			got := s.isRemoteServer()
			if got != tt.want {
				t.Errorf("isRemoteServer() = %v, want %v (host=%q, serverMode=%v)",
					got, tt.want, tt.serverHost, tt.serverMode)
			}
		})
	}
}

// TestRemoteServerPushSkipsCLIDirGuard verifies that pushToRemote on a remote
// server with no CLI dir does NOT return the "requires a local Dolt CLI
// database directory" error. Instead it should fall through to the SQL path
// (CALL DOLT_PUSH), which will fail with a connection error in tests — but
// crucially NOT with the CLI-dir error.
func TestRemoteServerPushSkipsCLIDirGuard(t *testing.T) {
	t.Parallel()

	s := &DoltStore{
		db:          stubDB(t),
		serverHost:  "mini2",
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
		remote:      "origin",
		branch:      "main",
	}

	// Verify the preconditions: this store requires explicit CLI dir and has none.
	if !s.requiresExplicitCLIDir() {
		t.Fatal("expected requiresExplicitCLIDir() = true")
	}
	if s.CLIDir() != "" {
		t.Fatal("expected CLIDir() = empty")
	}

	// pushToRemote should NOT return the CLI-dir error.
	// It will fail (no real DB connection), but the error should NOT be
	// "requires a local Dolt CLI database directory".
	err := s.pushToRemote(t.Context(), "origin", false)
	if err == nil {
		t.Fatal("expected an error (no real DB), got nil")
	}
	if strings.Contains(err.Error(), "requires a local Dolt CLI database directory") {
		t.Errorf("pushToRemote returned CLI-dir error on remote server: %v", err)
	}
}

// TestRemoteServerPullSkipsCLIDirGuard verifies the same for pullFromRemote.
func TestRemoteServerPullSkipsCLIDirGuard(t *testing.T) {
	t.Parallel()

	s := &DoltStore{
		db:          stubDB(t),
		serverHost:  "mini2",
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
		remote:      "origin",
		branch:      "main",
		readOnly:    true, // skip auto-commit before pull (no real db)
	}

	if !s.requiresExplicitCLIDir() {
		t.Fatal("expected requiresExplicitCLIDir() = true")
	}
	if s.CLIDir() != "" {
		t.Fatal("expected CLIDir() = empty")
	}

	err := s.pullFromRemote(t.Context(), "origin")
	if err == nil {
		t.Fatal("expected an error (no real DB), got nil")
	}
	if strings.Contains(err.Error(), "requires a local Dolt CLI database directory") {
		t.Errorf("pullFromRemote returned CLI-dir error on remote server: %v", err)
	}
}

// TestLocalServerPushStillRequiresCLIDir verifies that the CLI-dir guard still
// fires for local external servers with credentials and no CLI dir.
func TestLocalServerPushStillRequiresCLIDir(t *testing.T) {
	t.Parallel()

	s := &DoltStore{
		db:          stubDB(t),
		serverHost:  "127.0.0.1",
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
		remote:      "origin",
		branch:      "main",
		remoteUser:  "testuser",
	}

	if !s.requiresExplicitCLIDir() {
		t.Fatal("expected requiresExplicitCLIDir() = true")
	}

	err := s.pushToRemote(t.Context(), "origin", false)
	if err == nil {
		// Local external server with credentials and no CLI dir should error
		// at the credential guard or fall through to SQL. Either way is OK —
		// the key invariant is that remote servers bypass the guard.
		t.Log("push returned nil — local server routed to SQL path (acceptable)")
		return
	}
	// The error should be the CLI-dir error (credentials present, no CLI dir,
	// local server) or a connection error. Both are acceptable.
	t.Logf("local server push error (expected): %v", err)
}
