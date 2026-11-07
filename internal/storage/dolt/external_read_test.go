package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// Tests for NewExternalReadOnly (#5861 item 2): the strictly non-mutating
// connection a routed foreign read opens into a proxied-server target's
// external SQL server. The posture contract under test — see
// Config.ExternalReadOnly — is that the connection is built only from the
// sidecar's endpoint and the caller's explicit values: no ambient launcher
// environment, no creation, no auto-start, no mutation.

// externalReadOnlyFixture seeds one proxied-server target workspace whose
// external server is the test container, then hands back everything a test
// needs to open it externally.
type externalReadOnlyFixture struct {
	beadsDir  string
	database  string
	projectID string
	external  configfile.ExternalDoltConfig
	issueID   string
}

func newExternalReadOnlyFixture(t *testing.T) *externalReadOnlyFixture {
	t.Helper()
	ctx := context.Background()
	f := &externalReadOnlyFixture{
		beadsDir:  filepath.Join(t.TempDir(), ".beads"),
		database:  uniqueTestDBName(t),
		projectID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		external:  configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: testServerPort},
	}
	if err := os.MkdirAll(f.beadsDir, 0o755); err != nil {
		t.Fatalf("create target .beads: %v", err)
	}
	meta := configfile.Config{
		ProjectID:    f.projectID,
		DoltMode:     configfile.DoltModeProxiedServer,
		DoltDatabase: f.database,
	}
	if err := meta.Save(f.beadsDir); err != nil {
		t.Fatalf("save target metadata.json: %v", err)
	}
	// An ordinary writable open of the same database — the target rig's own
	// bd would have done this — creates it, seeds the workspace identity the
	// external open must verify, and leaves one issue to read back.
	seed, err := openStoreWithOwnBudget(t, &Config{
		Path:            filepath.Join(f.beadsDir, "dolt"),
		BeadsDir:        f.beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		Database:        f.database,
		CommitterName:   "test",
		CommitterEmail:  "test@test.com",
		MaxOpenConns:    1,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("seed target database: %v", err)
	}
	if err := seed.SetMetadata(ctx, "_project_id", f.projectID); err != nil {
		seed.Close()
		t.Fatalf("seed _project_id: %v", err)
	}
	if err := setConfigWithOwnBudget(t, seed, "issue_prefix", "gt"); err != nil {
		seed.Close()
		t.Fatalf("seed issue_prefix: %v", err)
	}
	issue := &types.Issue{
		Title:     "external read target",
		IssueType: types.TypeTask,
		Priority:  2,
		Status:    types.StatusOpen,
	}
	if err := seed.CreateIssue(ctx, issue, "tester"); err != nil {
		seed.Close()
		t.Fatalf("seed issue: %v", err)
	}
	f.issueID = issue.ID
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	return f
}

// openExternal is the fixture's NewExternalReadOnly plus the read-back that
// proves the open produced a usable connection, not just a nil error.
func (f *externalReadOnlyFixture) openExternal(t *testing.T, database, password string, external configfile.ExternalDoltConfig, beadsDir ...string) *DoltStore {
	t.Helper()
	dir := f.beadsDir
	if len(beadsDir) > 0 {
		dir = beadsDir[0]
	}
	store, err := NewExternalReadOnly(context.Background(), dir, database, external, password)
	if err != nil {
		t.Fatalf("NewExternalReadOnly: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	issue, err := store.GetIssue(context.Background(), f.issueID)
	if err != nil {
		t.Fatalf("read seeded issue through the external store: %v", err)
	}
	if issue == nil || issue.ID != f.issueID {
		t.Fatalf("read back the wrong issue: %+v", issue)
	}
	return store
}

func TestExternalReadOnlyConnection(t *testing.T) {
	skipIfNoServer(t)
	f := newExternalReadOnlyFixture(t)

	t.Run("reads the target's database over the sidecar's endpoint", func(t *testing.T) {
		f.openExternal(t, f.database, "", f.external)
	})

	t.Run("hostile launcher environment does not reach the connection", func(t *testing.T) {
		// The launcher's environment names a different endpoint, a different
		// database, and a password. None of it may reach the external read:
		// the connection fields come from the sidecar and the caller only,
		// and an explicitly empty password stays empty rather than falling
		// back to BEADS_DOLT_PASSWORD.
		t.Setenv("BEADS_DOLT_SERVER_HOST", "192.0.2.1")
		t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
		t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/nonexistent/beads.sock")
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "evil_db")
		t.Setenv("BEADS_DOLT_PASSWORD", "hunter2") // #nolint:gosec // test fixture, never a real credential
		f.openExternal(t, f.database, "", f.external)
	})

	t.Run("connects as the sidecar's scoped user", func(t *testing.T) {
		// A user granted only this database — the topology the sidecar
		// describes — reads it through the external open.
		db := rawTestConn(t, testServerPort)
		defer db.Close()
		for _, stmt := range []string{
			fmt.Sprintf("CREATE USER '%s'@'%%'", f.database),
			fmt.Sprintf("GRANT ALL ON `%s`.* TO '%s'@'%%'", f.database, f.database),
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		scoped := f.external
		scoped.User = f.database
		f.openExternal(t, f.database, "", scoped)
	})

	t.Run("missing database fails without creating it", func(t *testing.T) {
		missing := uniqueTestDBName(t)
		assertDatabaseNotExists(t, testServerPort, missing)
		_, err := NewExternalReadOnly(context.Background(), f.beadsDir, missing, f.external, "")
		if err == nil {
			t.Fatal("expected database-not-found error, got nil")
		}
		if !containsAny(err.Error(), "not found", "does not exist", "unknown database") {
			t.Fatalf("error should indicate database not found, got: %v", err)
		}
		assertDatabaseNotExists(t, testServerPort, missing)
	})

	t.Run("workspace identity mismatch is refused", func(t *testing.T) {
		foreignDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(foreignDir, 0o755); err != nil {
			t.Fatalf("create foreign .beads: %v", err)
		}
		foreignMeta := configfile.Config{
			ProjectID:    "ffffffff-1111-2222-3333-444444444444",
			DoltMode:     configfile.DoltModeProxiedServer,
			DoltDatabase: f.database,
		}
		if err := foreignMeta.Save(foreignDir); err != nil {
			t.Fatalf("save foreign metadata.json: %v", err)
		}
		_, err := NewExternalReadOnly(context.Background(), foreignDir, f.database, f.external, "")
		if err == nil {
			t.Fatal("expected identity mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
			t.Fatalf("expected PROJECT IDENTITY MISMATCH, got: %v", err)
		}
	})

	t.Run("socket-only endpoint stays socket-only", func(t *testing.T) {
		sock := configfile.ExternalDoltConfig{Socket: filepath.Join(t.TempDir(), "missing.sock")}
		_, err := NewExternalReadOnly(context.Background(), f.beadsDir, f.database, sock, "")
		if err == nil {
			t.Fatal("expected dial error for a socket nothing listens on, got nil")
		}
		if !strings.Contains(err.Error(), sock.Socket) {
			t.Fatalf("error should name the socket %s, got: %v", sock.Socket, err)
		}
		if strings.Contains(err.Error(), "tcp") {
			t.Fatalf("a socket-only endpoint must not fall back to TCP, got: %v", err)
		}
	})

	t.Run("writes are refused", func(t *testing.T) {
		store := f.openExternal(t, f.database, "", f.external)
		err := store.CreateIssue(context.Background(), &types.Issue{
			Title:     "must not land",
			IssueType: types.TypeTask,
			Priority:  2,
			Status:    types.StatusOpen,
		}, "tester")
		if !errors.Is(err, ErrExternalReadOnlyWrite) {
			t.Fatalf("write through the external store must fail with ErrExternalReadOnlyWrite, got: %v", err)
		}
		// Nothing landed: the issues table still holds exactly the seed.
		var n int
		if err := store.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM issues").Scan(&n); err != nil {
			t.Fatalf("count issues after refused write: %v", err)
		}
		if n != 1 {
			t.Fatalf("refused write must leave exactly the seeded issue, found %d", n)
		}
	})
}

// TestExternalReadOnlyConfigPosture pins the Config the constructor builds:
// the pins are the posture, so a future edit to externalReadOnlyConfig that
// loosens one fails here without needing a server.
func TestExternalReadOnlyConfigPosture(t *testing.T) {
	ext := configfile.ExternalDoltConfig{Host: "sql.example", Port: 13306, User: "scoped"}
	cfg := externalReadOnlyConfig("/w/town/rig/.beads", "gtdb", ext, "pw")
	if !cfg.ReadOnly || !cfg.RequireReadableSchema {
		t.Errorf("posture must pin ReadOnly and RequireReadableSchema: %+v", cfg)
	}
	if cfg.CreateIfMissing || !cfg.DisableAutoStart || !cfg.ExternalReadOnly {
		t.Errorf("posture must refuse creation and auto-start and mark itself: %+v", cfg)
	}
	if cfg.Gateway || cfg.ProxiedServer {
		t.Errorf("posture is neither gateway nor proxy ownership: %+v", cfg)
	}
	if cfg.ServerHost != ext.Host || cfg.ServerPort != ext.Port || cfg.ServerSocket != "" {
		t.Errorf("endpoint fields must come from the sidecar only: %+v", cfg)
	}
	if cfg.ServerUser != "scoped" || cfg.ServerPassword != "pw" {
		t.Errorf("credentials must be the caller's values verbatim: %+v", cfg)
	}
	defaultUser := externalReadOnlyConfig("/w/.beads", "db", configfile.ExternalDoltConfig{Host: "h", Port: 1}, "")
	if defaultUser.ServerUser != configfile.ExternalDoltConfigDefaultUser {
		t.Errorf("empty sidecar user must resolve to %s, got %q", configfile.ExternalDoltConfigDefaultUser, defaultUser.ServerUser)
	}
	if defaultUser.ServerPassword != "" {
		t.Errorf("empty password must stay empty (authoritative), got %q", defaultUser.ServerPassword)
	}
}

// TestExternalReadOnlyValidation covers the refusals that happen before any
// dial: they need no server.
func TestExternalReadOnlyValidation(t *testing.T) {
	valid := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 1}
	for _, tc := range []struct {
		name             string
		beadsDir, db     string
		external         configfile.ExternalDoltConfig
		wantErrSubstring string
	}{
		{"no workspace", "", "db", valid, "workspace"},
		{"no database", "/w/.beads", "", valid, "database"},
		{"invalid external endpoint", "/w/.beads", "db", configfile.ExternalDoltConfig{Host: "h"}, "Host requires Port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewExternalReadOnly(context.Background(), tc.beadsDir, tc.db, tc.external, "")
			if err == nil {
				t.Fatalf("expected refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Fatalf("error should contain %q, got: %v", tc.wantErrSubstring, err)
			}
		})
	}
}

// TestExternalReadOnlyDSNTLSConfigName pins the DSN spelling of the TLS
// handoff: a sidecar-grade named TLS config replaces the anonymous tls=true,
// and an unnamed one keeps it.
func TestExternalReadOnlyDSNTLSConfigName(t *testing.T) {
	base := func() *Config {
		return &Config{ServerHost: "sql.example", ServerPort: 13306, ServerUser: "root", ServerTLS: true}
	}
	named := base()
	named.ServerTLSConfigName = "beads-external-test"
	if got := buildServerDSN(named, "db"); !strings.Contains(got, "tls=beads-external-test") {
		t.Fatalf("named TLS config must reach the DSN, got: %s", got)
	}
	if got := buildServerDSN(base(), "db"); !strings.Contains(got, "tls=true") {
		t.Fatalf("unnamed TLS keeps tls=true, got: %s", got)
	}
}

// TestExternalReadOnlyNoMutation pins the strictly non-mutating half of the
// external-read posture (#5861 item 2, AC-4): a successful external read of a
// proxied target changes nothing — not one file in the target workspace (no
// proxy root, no auto-start port file, no local data directory), no dolt
// commit, no schema migration row — and an open whose database is missing
// creates neither the database nor any local state.
func TestExternalReadOnlyNoMutation(t *testing.T) {
	skipIfNoServer(t)
	f := newExternalReadOnlyFixture(t)
	ctx := context.Background()

	// snapshot records every file under the fixture's workspace root.
	snapshot := func() map[string]int64 {
		t.Helper()
		files := map[string]int64{}
		err := filepath.WalkDir(filepath.Dir(f.beadsDir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(filepath.Dir(f.beadsDir), path)
			if err != nil {
				return err
			}
			files[rel] = info.Size()
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot workspace files: %v", err)
		}
		return files
	}
	// serverState reads the row counts an unnoticed write or migration would
	// move: dolt commits and applied schema migrations.
	serverState := func() (commits, migrations int) {
		t.Helper()
		db := rawTestConn(t, testServerPort)
		defer db.Close()
		var mig int
		if err := db.QueryRow("SELECT COUNT(*) FROM `" + f.database + "`.schema_migrations").Scan(&mig); err != nil {
			t.Fatalf("count schema_migrations: %v", err)
		}
		var com int
		if err := db.QueryRow("SELECT COUNT(*) FROM `" + f.database + "`.dolt_log").Scan(&com); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		return com, mig
	}

	filesBefore := snapshot()
	commitsBefore, migrationsBefore := serverState()

	store, err := NewExternalReadOnly(ctx, f.beadsDir, f.database, f.external, "")
	if err != nil {
		t.Fatalf("NewExternalReadOnly: %v", err)
	}
	if _, err := store.GetIssue(ctx, f.issueID); err != nil {
		t.Fatalf("read seeded issue: %v", err)
	}
	if _, err := store.GetMetadata(ctx, "_project_id"); err != nil {
		t.Fatalf("read workspace identity: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close external store: %v", err)
	}

	if filesAfter := snapshot(); !reflect.DeepEqual(filesBefore, filesAfter) {
		t.Errorf("external read changed the target workspace:\nbefore: %v\nafter:  %v", filesBefore, filesAfter)
	}
	if _, err := os.Stat(filepath.Join(f.beadsDir, "dolt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("external read must not create a local data directory at %s: %v", filepath.Join(f.beadsDir, "dolt"), err)
	}
	commitsAfter, migrationsAfter := serverState()
	if commitsAfter != commitsBefore {
		t.Errorf("external read minted dolt commits: %d -> %d", commitsBefore, commitsAfter)
	}
	if migrationsAfter != migrationsBefore {
		t.Errorf("external read applied schema migrations: %d -> %d", migrationsBefore, migrationsAfter)
	}

	// A read whose database is missing fails without creating anything on
	// either side: not the database, not local state in a fresh workspace.
	missing := uniqueTestDBName(t)
	freshDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(freshDir, 0o755); err != nil {
		t.Fatalf("create fresh .beads: %v", err)
	}
	assertDatabaseNotExists(t, testServerPort, missing)
	if _, err := NewExternalReadOnly(ctx, freshDir, missing, f.external, ""); err == nil {
		t.Fatal("expected database-not-found error, got nil")
	}
	assertDatabaseNotExists(t, testServerPort, missing)
	entries, err := os.ReadDir(freshDir)
	if err != nil {
		t.Fatalf("read fresh .beads: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed external read must leave the workspace empty, found %v", entries)
	}
}
