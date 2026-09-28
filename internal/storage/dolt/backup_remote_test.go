package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// remoteBackupGuardMsg is the error substring that the remote server guard
// should produce. Using a const keeps the red tests and green fix in sync.
const remoteBackupGuardMsg = "not supported for remote dolt servers"

// TestBackupDatabaseRejectsRemoteServer verifies that BackupDatabase returns
// an error when connected to a remote Dolt server. Sending file:// URLs to a
// remote server would create directories on the remote filesystem.
//
// User story (docs/REMOTE_SERVER_USER_STORIES.md - Backup):
//
//	Given beads is connected to a remote dolt server
//	When I run `bd backup init /some/local/path`
//	Then beads does NOT send that local path to the remote server
func TestBackupDatabaseRejectsRemoteServer(t *testing.T) {
	t.Parallel()

	s := &DoltStore{
		db:          stubDB(t),
		serverHost:  "mini2",
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
	}

	err := s.BackupDatabase(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("expected BackupDatabase to return error on remote server, got nil")
	}
	if !strings.Contains(err.Error(), remoteBackupGuardMsg) {
		t.Errorf("expected error containing %q, got: %v", remoteBackupGuardMsg, err)
	}
}

// TestBackupDatabaseAllowsLocalServer verifies that BackupDatabase does NOT
// reject local server connections. The file:// URL is valid when server and
// client share a filesystem.
//
// User story:
//
//	Given beads is connected to a local dolt server (localhost)
//	When I run `bd backup init /some/local/path`
//	Then the backup is created at that path as normal
func TestBackupDatabaseAllowsLocalServer(t *testing.T) {
	t.Parallel()

	s := &DoltStore{
		db:          stubDB(t),
		serverHost:  "127.0.0.1",
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
	}

	// BackupDatabase on a local server should NOT return the remote guard error.
	// It will fail for other reasons (fake DB), but not with the remote guard.
	err := s.BackupDatabase(t.Context(), t.TempDir())
	if err != nil && strings.Contains(err.Error(), remoteBackupGuardMsg) {
		t.Errorf("BackupDatabase should not reject local server, got: %v", err)
	}
}

// TestRestoreDatabaseRejectsRemoteServer verifies that RestoreDatabase also
// rejects remote servers. Same file:// URL problem applies to restore.
func TestRestoreDatabaseRejectsRemoteServer(t *testing.T) {
	t.Parallel()

	s := &DoltStore{
		db:          stubDB(t),
		serverHost:  "mini2",
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
	}

	err := s.RestoreDatabase(t.Context(), t.TempDir(), false)
	if err == nil {
		t.Fatal("expected RestoreDatabase to return error on remote server, got nil")
	}
	if !strings.Contains(err.Error(), remoteBackupGuardMsg) {
		t.Errorf("expected error containing %q, got: %v", remoteBackupGuardMsg, err)
	}
}
