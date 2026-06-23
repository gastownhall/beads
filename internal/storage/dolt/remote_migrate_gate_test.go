package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestDoltNew_RemoteMigrateGate_BlocksReopen is a full-chain integration test:
// opening a read/write server-mode store against an existing, remote-backed
// database that is behind the binary must refuse to auto-migrate and return a
// *schema.RemoteMigrateGateError instead of silently forking the schema (#4259).
//
// It covers both ways the database can be detected as remote-backed, reusing one
// CREATE/DROP DATABASE cycle (consecutive create/drop cycles destabilize the test
// dolt server, so a second integration test is deliberately avoided):
//   - disk path (gastownhall/beads#4268): a CLI remote persisted in .dolt/config
//     while the SQL dolt_remotes table is still empty — the state of a freshly
//     (auto-)started server before syncCLIRemotesToSQL runs;
//   - SQL path: a remote registered in dolt_remotes.
func TestDoltNew_RemoteMigrateGate_BlocksReopen(t *testing.T) {
	skipIfNoDolt(t)
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	// Create and fully migrate the database.
	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// Simulate an existing database one migration behind this binary by dropping
	// the latest cursor row (the schema change itself stays applied; only the
	// recorded version regresses, so the gate sees a pending migration).
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}

	reopen := func() error {
		s, err := New(ctx, &Config{
			Path:           tmpDir,
			CommitterName:  "test",
			CommitterEmail: "test@example.com",
			Database:       dbName,
		})
		if err == nil {
			s.Close()
		}
		return err
	}

	// Disk path (#4268): persist a remote ONLY on disk (.dolt/config) at the
	// database CLI dir, leaving the SQL dolt_remotes table empty. This is the
	// freshly (auto-)started-server state, where syncCLIRemotesToSQL has not yet
	// run; an SQL-only gate would miss the remote and migrate in place.
	cliDir := store.CLIDir()
	if cliDir == "" {
		t.Skip("no CLI dir available")
	}
	initLocalDoltRepoForRemote(t, cliDir)
	if err := doltutil.AddCLIRemote(cliDir, "origin", "file://"+filepath.Join(tmpDir, "remote")); err != nil {
		t.Fatalf("AddCLIRemote: %v", err)
	}
	// Precondition: no remote is visible in SQL, so this reopen can only trip the
	// gate via the on-disk fallback.
	if remotes, err := store.ListRemotes(ctx); err != nil {
		t.Fatalf("ListRemotes: %v", err)
	} else if len(remotes) != 0 {
		t.Fatalf("precondition failed: SQL dolt_remotes already has %d remote(s); disk fallback not exercised", len(remotes))
	}
	if reErr := reopen(); reErr == nil {
		t.Fatal("New (reopen, disk-only remote) = nil, want *schema.RemoteMigrateGateError")
	} else if !schema.IsRemoteMigrateGateError(reErr) {
		t.Fatalf("disk-only remote: error = %T (%v), want *schema.RemoteMigrateGateError", reErr, reErr)
	}

	// SQL path (#4259): register the remote in dolt_remotes too and confirm the
	// gate still fires through the original server-session check.
	if _, err := store.db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(tmpDir, "remote")); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	if reErr := reopen(); reErr == nil {
		t.Fatal("New (reopen, SQL remote) = nil, want *schema.RemoteMigrateGateError for a behind, remote-backed DB")
	} else if !schema.IsRemoteMigrateGateError(reErr) {
		t.Fatalf("SQL remote: error = %T (%v), want error wrapping *schema.RemoteMigrateGateError", reErr, reErr)
	}
}

// initLocalDoltRepoForRemote prepares dir as a standalone Dolt repository so CLI
// remote commands (dolt remote add / dolt remote -v) operate on its .dolt/config.
// Mirrors the setup used by TestSyncCLIRemotesToSQL.
func initLocalDoltRepoForRemote(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
	cmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "already") {
		t.Fatalf("dolt init in %q failed: %s: %v", dir, out, err)
	}
}

// TestDoltStore_hasPersistedCLIRemote verifies the on-disk remote probe the #4259
// gate uses in server mode: it must detect a remote persisted in .dolt/config even
// when nothing is registered in the SQL dolt_remotes table — the state of a freshly
// (auto-)started server before syncCLIRemotesToSQL runs (gastownhall/beads#4268).
// It needs only the dolt binary, not the test server.
func TestDoltStore_hasPersistedCLIRemote(t *testing.T) {
	testutil.RequireDoltBinary(t)

	root := t.TempDir()
	// serverMode + empty beadsDir makes CLIDir() == filepath.Join(dbPath, database),
	// independent of shared-server resolution.
	s := &DoltStore{dbPath: root, database: "testdb", serverMode: true}

	cliDir := s.CLIDir()
	initLocalDoltRepoForRemote(t, cliDir)

	if s.hasPersistedCLIRemote() {
		t.Fatal("hasPersistedCLIRemote() = true before any remote was added, want false")
	}

	const name, url = "origin", "file:///tmp/test-haspersisted-remote"
	if err := doltutil.AddCLIRemote(cliDir, name, url); err != nil {
		t.Fatalf("AddCLIRemote: %v", err)
	}

	if !s.hasPersistedCLIRemote() {
		t.Fatal("hasPersistedCLIRemote() = false after a CLI remote was added, want true")
	}
}
