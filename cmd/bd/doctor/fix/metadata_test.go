package fix

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestFixMissingMetadata_NotBeadsWorkspace verifies that FixMissingMetadata
// returns an error for paths without a .beads directory.
func TestFixMissingMetadata_NotBeadsWorkspace(t *testing.T) {
	dir := t.TempDir()
	err := FixMissingMetadata(dir, "1.0.0")
	if err == nil {
		t.Error("expected error for non-beads workspace")
	}
}

// TestFixMissingMetadata_NoConfig verifies that FixMissingMetadata returns nil
// when there is no metadata.json config file (nothing to fix).
func TestFixMissingMetadata_NoConfig(t *testing.T) {
	dir := setupTestWorkspace(t)
	err := FixMissingMetadata(dir, "1.0.0")
	if err != nil {
		t.Errorf("expected nil for workspace without config, got: %v", err)
	}
}

// TestFixMissingMetadata_EmptyBackend verifies that FixMissingMetadata
// handles an empty backend field (defaults to dolt).
func TestFixMissingMetadata_EmptyBackend(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Create a config with empty backend (defaults to dolt)
	cfg := &configfile.Config{
		Database: "dolt",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Without a dolt directory, this should fail gracefully
	err := FixMissingMetadata(dir, "1.0.0")
	t.Logf("FixMissingMetadata result: %v", err)
}

// TestFixMissingMetadata_DoltConfigExists verifies that FixMissingMetadata
// handles a Dolt config correctly. On CGO builds, the factory bootstraps a
// new database even from an empty directory. On non-CGO builds, the backend
// is not registered so we get an error. Both outcomes are acceptable.
func TestFixMissingMetadata_DoltConfigExists(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Set up git repo so repo_id can be computed
	setupGitRepoInDir(t, dir)

	// Create Dolt config but no actual Dolt database
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Create the dolt directory
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	err := FixMissingMetadata(dir, "1.0.0")
	// On CGO builds: factory bootstraps a new DB, fix succeeds (nil).
	// On non-CGO builds: "unknown storage backend" error.
	// Both are acceptable behaviors.
	t.Logf("FixMissingMetadata result: %v", err)
}

func TestFixProjectIdentity_NotBeadsWorkspace(t *testing.T) {
	if err := FixProjectIdentity(t.TempDir()); err == nil {
		t.Fatal("expected error for non-beads workspace")
	}
}

func TestFixMissingDoltDatabase_NotBeadsWorkspace(t *testing.T) {
	if err := FixMissingDoltDatabase(t.TempDir()); err == nil {
		t.Fatal("expected error for non-beads workspace")
	}
}

// TestFixMissingMetadataJSON_Regenerates verifies that FixMissingMetadataJSON
// creates a metadata.json file when it's missing but .beads/ exists (GH#2478).
func TestFixMissingMetadataJSON_Regenerates(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	configPath := filepath.Join(beadsDir, "metadata.json")

	// Verify metadata.json does not exist
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatal("metadata.json should not exist before fix")
	}

	// Run the fix
	if err := FixMissingMetadataJSON(dir); err != nil {
		t.Fatalf("FixMissingMetadataJSON failed: %v", err)
	}

	// Verify metadata.json was created
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("metadata.json should exist after fix")
	}

	// Verify it contains valid config
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load regenerated config: %v", err)
	}
	if cfg == nil {
		t.Fatal("Loaded config should not be nil")
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		t.Errorf("Backend = %q, want %q", cfg.GetBackend(), configfile.BackendDolt)
	}
}

// TestFixMissingMetadataJSON_NoOpWhenPresent verifies that FixMissingMetadataJSON
// does nothing when metadata.json already exists.
func TestFixMissingMetadataJSON_NoOpWhenPresent(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Create metadata.json with custom content
	cfg := &configfile.Config{
		Database:  "custom-db",
		Backend:   configfile.BackendDolt,
		DoltMode:  "embedded",
		ProjectID: "test-project-123",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Run the fix — should be a no-op
	if err := FixMissingMetadataJSON(dir); err != nil {
		t.Fatalf("FixMissingMetadataJSON failed: %v", err)
	}

	// Verify original config is preserved (not overwritten with defaults)
	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if loaded.ProjectID != "test-project-123" {
		t.Errorf("ProjectID = %q, want %q (fix should not overwrite existing config)", loaded.ProjectID, "test-project-123")
	}
}

func TestFixMissingMetadataJSON_SharedWorktreeFallback(t *testing.T) {
	mainRepoDir, worktreeDir := setupSharedWorktreeWorkspace(t)
	sharedBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(sharedBeadsDir, 0o755); err != nil {
		t.Fatalf("failed to create shared .beads dir: %v", err)
	}

	configPath := filepath.Join(sharedBeadsDir, "metadata.json")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatal("metadata.json should not exist before fix")
	}

	if err := FixMissingMetadataJSON(worktreeDir); err != nil {
		t.Fatalf("FixMissingMetadataJSON failed: %v", err)
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("metadata.json should exist in shared .beads after fix")
	}
	if _, err := os.Stat(filepath.Join(worktreeDir, ".beads", "metadata.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no worktree-local metadata.json, got err=%v", err)
	}
}

// TestFixMissingMetadataJSON_NotBeadsWorkspace verifies that FixMissingMetadataJSON
// returns an error for paths without a .beads directory.
func TestFixMissingMetadataJSON_NotBeadsWorkspace(t *testing.T) {
	dir := t.TempDir()
	err := FixMissingMetadataJSON(dir)
	if err == nil {
		t.Error("expected error for non-beads workspace")
	}
}

// setupGitRepoInDir initializes a git repo in the given directory with a remote.
//
// Every step is fatal on failure, the dir must exist, and the child env is
// scrubbed of the git vars that name a repository (GIT_DIR, GIT_WORK_TREE,
// ...). An empty dir, a swallowed config failure, or a leaked GIT_DIR each make
// these writes land in whatever repository git resolves from the process
// environment — the host checkout's own .git/config in the r880 clobber
// (wy-qlvlmf), where this helper's remote.origin.url overwrote the real origin.
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()
	if dir == "" {
		t.Fatal("setupGitRepoInDir: empty dir would run git against the host checkout")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("setupGitRepoInDir: %q is not an existing directory (err=%v)", dir, err)
	}
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "core.hooksPath", ".git/hooks"},
		{"git", "config", "remote.origin.url", "https://github.com/test/metadata-test.git"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
}

func TestReconcileAuthoritativeServerMetadata_UsesProjectIDToRepairDatabaseName(t *testing.T) {
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "wrong_db",
		ProjectID:    "proj-123",
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "wrong_db", HasSchema: true, ProjectID: ""},
		{Name: "canonical_db", HasSchema: true, ProjectID: "proj-123"},
	})
	if err != nil {
		t.Fatalf("reconcileAuthoritativeServerMetadata error: %v", err)
	}
	if !changed {
		t.Fatal("expected repair to change metadata")
	}
	if cfg.DoltDatabase != "canonical_db" {
		t.Fatalf("DoltDatabase = %q, want %q", cfg.DoltDatabase, "canonical_db")
	}
	if !strings.Contains(msg, "canonical_db") || !strings.Contains(msg, "proj-123") {
		t.Fatalf("unexpected repair message: %q", msg)
	}
}

func TestReconcileAuthoritativeServerMetadata_ErrorsOnConflictingIdentitySignals(t *testing.T) {
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "current_db",
		ProjectID:    "stale-local-id",
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "current_db", HasSchema: true, ProjectID: "server-authoritative-id"},
		{Name: "other_db", HasSchema: true, ProjectID: "stale-local-id"},
	})
	if err == nil {
		t.Fatal("expected conflict error when project_id and configured database disagree")
	}
	if changed {
		t.Fatal("changed = true, want false on conflict")
	}
	if msg != "" {
		t.Fatalf("msg = %q, want empty on conflict", msg)
	}
	if !strings.Contains(err.Error(), "conflicting project identity") {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DoltDatabase != "current_db" {
		t.Fatalf("DoltDatabase mutated to %q, want %q", cfg.DoltDatabase, "current_db")
	}
	if cfg.ProjectID != "stale-local-id" {
		t.Fatalf("ProjectID mutated to %q, want %q", cfg.ProjectID, "stale-local-id")
	}
}

func TestReconcileAuthoritativeServerMetadata_AdoptsConfiguredDatabaseProjectID(t *testing.T) {
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "shared_db",
		ProjectID:    "stale-local-id",
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "shared_db", HasSchema: true, ProjectID: "server-authoritative-id"},
	})
	if err != nil {
		t.Fatalf("reconcileAuthoritativeServerMetadata error: %v", err)
	}
	if !changed {
		t.Fatal("expected repair to change metadata")
	}
	if cfg.ProjectID != "server-authoritative-id" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "server-authoritative-id")
	}
	if !strings.Contains(msg, "shared_db") || !strings.Contains(msg, "server-authoritative-id") {
		t.Fatalf("unexpected repair message: %q", msg)
	}
}

func TestReconcileAuthoritativeServerMetadata_ErrorsOnAmbiguousProjectIDMatch(t *testing.T) {
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "wrong_db",
		ProjectID:    "proj-123",
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "canonical_a", HasSchema: true, ProjectID: "proj-123"},
		{Name: "canonical_b", HasSchema: true, ProjectID: "proj-123"},
	})
	if err == nil {
		t.Fatal("expected ambiguous project_id match error")
	}
	if changed {
		t.Fatal("changed = true, want false on error")
	}
	if msg != "" {
		t.Fatalf("msg = %q, want empty", msg)
	}
}

func TestReconcileAuthoritativeServerMetadata_SoleCandidateFallback(t *testing.T) {
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "wrong_db",
		// No ProjectID — triggers sole-candidate fallback
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "only_db", HasSchema: true, ProjectID: "discovered-proj"},
	})
	if err != nil {
		t.Fatalf("reconcileAuthoritativeServerMetadata error: %v", err)
	}
	if !changed {
		t.Fatal("expected repair to change metadata")
	}
	if cfg.DoltDatabase != "only_db" {
		t.Fatalf("DoltDatabase = %q, want %q", cfg.DoltDatabase, "only_db")
	}
	if cfg.ProjectID != "discovered-proj" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "discovered-proj")
	}
	if !strings.Contains(msg, "only server database") {
		t.Fatalf("unexpected msg: %q", msg)
	}
}

func TestReconcileAuthoritativeServerMetadata_DoesNotAdoptSoleCandidateInSharedServerMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "wrong_db",
		// No ProjectID; in shared-server mode this must not trigger candidate adoption.
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "only_db", HasSchema: true, ProjectID: "other-project"},
	})
	if err != nil {
		t.Fatalf("reconcileAuthoritativeServerMetadata error: %v", err)
	}
	if changed {
		t.Fatalf("expected no change in shared-server mode, got msg: %q", msg)
	}
	if msg != "" {
		t.Fatalf("expected empty msg when no change, got %q", msg)
	}
	if cfg.DoltDatabase != "wrong_db" {
		t.Fatalf("DoltDatabase = %q, want unchanged %q", cfg.DoltDatabase, "wrong_db")
	}
	if cfg.ProjectID != "" {
		t.Fatalf("ProjectID = %q, want empty", cfg.ProjectID)
	}
}

func TestReconcileAuthoritativeServerMetadata_NoChangeWhenAlreadyCorrect(t *testing.T) {
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "correct_db",
		ProjectID:    "proj-123",
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "correct_db", HasSchema: true, ProjectID: "proj-123"},
	})
	if err != nil {
		t.Fatalf("reconcileAuthoritativeServerMetadata error: %v", err)
	}
	if changed {
		t.Fatalf("expected no change, got msg: %q", msg)
	}
}

func TestReconcileAuthoritativeServerMetadata_BackfillsEmptyProjectID(t *testing.T) {
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "shared_db",
		// No ProjectID
	}

	changed, msg, err := reconcileAuthoritativeServerMetadata(cfg, []serverDatabaseMetadata{
		{Name: "shared_db", HasSchema: true, ProjectID: "server-id"},
		{Name: "other_db", HasSchema: true, ProjectID: "other-id"},
	})
	if err != nil {
		t.Fatalf("reconcileAuthoritativeServerMetadata error: %v", err)
	}
	if !changed {
		t.Fatal("expected repair to change metadata")
	}
	if cfg.ProjectID != "server-id" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "server-id")
	}
	if !strings.Contains(msg, "backfilled") {
		t.Fatalf("expected backfill message, got: %q", msg)
	}
}

func TestResolveAuthoritativeServerMetadata_DryRunDoesNotSave(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	cfg := &configfile.Config{
		DoltMode:     configfile.DoltModeServer,
		DoltDatabase: "wrong_db",
		ProjectID:    "proj-123",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	origList := listServerMetadataDatabases
	listServerMetadataDatabases = func(_ string, _ *configfile.Config) ([]serverDatabaseMetadata, error) {
		return []serverDatabaseMetadata{
			{Name: "canonical_db", HasSchema: true, ProjectID: "proj-123"},
		}, nil
	}
	defer func() { listServerMetadataDatabases = origList }()

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	resultCfg, msg, err := ResolveAuthoritativeServerMetadata(dir, false)
	if err != nil {
		t.Fatalf("ResolveAuthoritativeServerMetadata failed: %v", err)
	}
	if !strings.HasPrefix(msg, "would ") {
		t.Fatalf("dry-run msg should start with 'would ', got: %q", msg)
	}

	// Verify the on-disk config was NOT changed
	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if loaded.DoltDatabase != "wrong_db" {
		t.Fatalf("on-disk DoltDatabase changed to %q during dry-run", loaded.DoltDatabase)
	}
	// But the returned config should reflect the repair
	if resultCfg.DoltDatabase != "canonical_db" {
		t.Fatalf("returned DoltDatabase = %q, want %q", resultCfg.DoltDatabase, "canonical_db")
	}
}

func TestResolveAuthoritativeServerMetadata_RunsInSharedServerModeWithoutServerDoltMode(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	cfg := configfile.DefaultConfig()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	origList := listServerMetadataDatabases
	called := false
	listServerMetadataDatabases = func(beadsDir string, cfg *configfile.Config) ([]serverDatabaseMetadata, error) {
		called = true
		return nil, nil
	}
	defer func() { listServerMetadataDatabases = origList }()

	if _, _, err := ResolveAuthoritativeServerMetadata(dir, false); err != nil {
		t.Fatalf("ResolveAuthoritativeServerMetadata failed: %v", err)
	}
	if !called {
		t.Fatal("expected shared-server metadata probe to run")
	}
}

func TestIsExpectedProbeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, true},
		{"ErrNoRows", sql.ErrNoRows, true},
		{"table not exist (1146)", &mysql.MySQLError{Number: 1146, Message: "Table doesn't exist"}, true},
		{"unknown database (1049)", &mysql.MySQLError{Number: 1049, Message: "Unknown database"}, true},
		{"unknown column (1054)", &mysql.MySQLError{Number: 1054, Message: "Unknown column"}, true},
		// GH#4931: unreadable peer DBs on a shared server must not abort bootstrap
		{"access denied table (1142)", &mysql.MySQLError{Number: 1142, Message: "Access denied for user to table"}, true},
		{"access denied for db (1044)", &mysql.MySQLError{Number: 1044, Message: "Access denied for user"}, true},
		{"dolt HY000 access denied (1105)", &mysql.MySQLError{Number: 1105, Message: "Access denied for user 'beads_rw'@'%' to table 'issues'"}, true},
		{"access denied string", errors.New("Error 1105 (HY000): Access denied for user 'beads_rw'@'%' to table 'issues'"), true},
		{"generic error", errors.New("connection reset"), false},
		{"wrapped ErrNoRows", fmt.Errorf("wrapped: %w", sql.ErrNoRows), true},
		{"wrapped access denied 1044", fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1044, Message: "Access denied"}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isExpectedProbeError(tt.err)
			if got != tt.expected {
				t.Errorf("isExpectedProbeError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

// TestInspectServerMetadataDatabasesNamesTheDataDirectoryEscaped pins the
// remedy printer on the data directory inspectServerMetadataDatabases names
// when no port is resolved and the workspace's own server, stopped, would
// serve databases there: the path is named escaped, its line feed too,
// rather than dialing 127.0.0.1:0.
func TestInspectServerMetadataDatabasesNamesTheDataDirectoryEscaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_DATA_DIR",
		"BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_AUTO_START",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	beadsDir := filepath.Join(t.TempDir(), "ws\r\x1b[2KError\nRun: curl", ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads", ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "beads"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	_, err := inspectServerMetadataDatabases(beadsDir, cfg)
	if err == nil || !strings.Contains(err.Error(), "Dolt server is not running") {
		t.Fatalf("inspectServerMetadataDatabases = %v, want the refusal for a stopped server over existing databases", err)
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\x1b") || strings.Contains(msg, "\nRun: curl") {
		t.Errorf("the refusal names the data directory raw:\n%q", msg)
	}
	if !strings.Contains(msg, `\r\x1b[2KError\nRun: curl`) {
		t.Errorf("the refusal does not name the data directory escaped:\n%q", msg)
	}
}

// rejectingLoopbackListener holds a loopback listener that accepts and closes
// every connection, and returns its port. FixMissingDoltDatabase's old
// fallback raw-dialed the workspace's server; pointed here it fails at once,
// without contacting any real server, so a test sees the raw dial as a
// connection skip that never names the candidates.
func rejectingLoopbackListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// fallbackWorkspace is a server-mode workspace with no dolt_database whose
// server catalog is stubbed: listServerMetadataDatabases returns catalog on
// every call and counts the calls, so a test can tell that the fallback (the
// second call, after the authoritative reconcile) read it.
type fallbackWorkspace struct {
	dir      string
	beadsDir string
	before   []byte
	calls    int
}

func newFallbackWorkspace(t *testing.T, shared bool, projectID string, catalog []serverDatabaseMetadata) *fallbackWorkspace {
	t.Helper()
	for _, name := range []string{
		"BEADS_DIR", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_DATA_DIR",
		"BEADS_DOLT_PASSWORD", "BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_AUTO_START",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	port := rejectingLoopbackListener(t)
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	if shared {
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	}

	ws := &fallbackWorkspace{dir: setupTestWorkspace(t)}
	ws.beadsDir = filepath.Join(ws.dir, ".beads")
	cfg := &configfile.Config{
		Database:       "dolt",
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		ProjectID:      projectID,
	}
	if err := cfg.Save(ws.beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(ws.beadsDir, configfile.ConfigFileName))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	ws.before = before

	orig := listServerMetadataDatabases
	listServerMetadataDatabases = func(_ string, _ *configfile.Config) ([]serverDatabaseMetadata, error) {
		ws.calls++
		return append([]serverDatabaseMetadata(nil), catalog...), nil
	}
	t.Cleanup(func() { listServerMetadataDatabases = orig })
	return ws
}

// assertUnchanged fails unless metadata.json still holds its original bytes.
func (ws *fallbackWorkspace) assertUnchanged(t *testing.T) {
	t.Helper()
	after, err := os.ReadFile(filepath.Join(ws.beadsDir, configfile.ConfigFileName))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	if !bytes.Equal(after, ws.before) {
		t.Fatalf("metadata.json changed:\nbefore: %s\nafter:  %s", ws.before, after)
	}
}

// assertFallbackRead fails unless both the authoritative reconcile and the
// fallback read the catalog.
func (ws *fallbackWorkspace) assertFallbackRead(t *testing.T) {
	t.Helper()
	if ws.calls != 2 {
		t.Fatalf("server catalog read %d time(s), want 2 (reconcile, then the fallback)", ws.calls)
	}
}

// assertPolicySkip fails unless err is a SkipError whose text names every
// want string and the manual remedy.
func assertPolicySkip(t *testing.T, err error, want ...string) {
	t.Helper()
	var skip *SkipError
	if !errors.As(err, &skip) {
		t.Fatalf("FixMissingDoltDatabase = %v, want a SkipError", err)
	}
	msg := err.Error()
	for _, w := range append(want, "bd dolt set database <name>") {
		if !strings.Contains(msg, w) {
			t.Errorf("skip %q does not name %q", msg, w)
		}
	}
}

// TestFixMissingDoltDatabase_SharedServerDeclinesUnanchoredCandidate pins
// GH#5576: in shared-server mode with no local project_id, the authoritative
// reconcile declines to adopt a schema database (it may be another
// project's), and the fallback must decline too rather than adopt it by
// enumeration order. It leaves metadata.json alone and names the candidates
// and the manual remedy.
func TestFixMissingDoltDatabase_SharedServerDeclinesUnanchoredCandidate(t *testing.T) {
	tests := []struct {
		name    string
		catalog []serverDatabaseMetadata
		want    []string
	}{
		{
			name: "sole foreign candidate",
			catalog: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase},
				{Name: "other_project", HasSchema: true, ProjectID: "other-proj-id"},
			},
			want: []string{"other_project"},
		},
		{
			name: "several candidates",
			catalog: []serverDatabaseMetadata{
				{Name: "zeta_project", HasSchema: true},
				{Name: "alpha_project", HasSchema: true, ProjectID: "alpha-id"},
			},
			want: []string{"alpha_project, zeta_project"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newFallbackWorkspace(t, true, "", tt.catalog)
			err := FixMissingDoltDatabase(ws.dir)
			assertPolicySkip(t, err, append(tt.want, "shared-server")...)
			ws.assertUnchanged(t)
			ws.assertFallbackRead(t)
		})
	}
}

// TestFixMissingDoltDatabase_KnownProjectIDDeclinesUnmatchedCandidates pins
// that a workspace which knows its project_id never adopts a database that
// does not carry it, in either server mode: not another project's, and not
// a sole database with no identity at all (absence of an identity is not a
// match, and the store's identity check tolerates it).
func TestFixMissingDoltDatabase_KnownProjectIDDeclinesUnmatchedCandidates(t *testing.T) {
	const localID = "local-proj-id"
	catalogs := []struct {
		name    string
		catalog []serverDatabaseMetadata
		want    []string
	}{
		{
			name: "sole candidate without identity",
			catalog: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase},
				{Name: "legacy_db", HasSchema: true},
			},
			want: []string{"legacy_db"},
		},
		{
			name: "sole foreign candidate",
			catalog: []serverDatabaseMetadata{
				{Name: "other_project", HasSchema: true, ProjectID: "other-proj-id"},
			},
			want: []string{"other_project"},
		},
		{
			name: "several unmatched candidates",
			catalog: []serverDatabaseMetadata{
				{Name: "zeta_project", HasSchema: true, ProjectID: "zeta-id"},
				{Name: "alpha_project", HasSchema: true},
			},
			want: []string{"alpha_project, zeta_project"},
		},
	}
	for _, shared := range []bool{false, true} {
		for _, tt := range catalogs {
			t.Run(fmt.Sprintf("shared=%t/%s", shared, tt.name), func(t *testing.T) {
				ws := newFallbackWorkspace(t, shared, localID, tt.catalog)
				err := FixMissingDoltDatabase(ws.dir)
				assertPolicySkip(t, err, append(tt.want, localID)...)
				ws.assertUnchanged(t)
				ws.assertFallbackRead(t)
			})
		}
	}
}

// TestFixMissingDoltDatabase_DeclinesAmbiguousCandidates pins that with no
// project_id to tell them apart, the fallback never picks one of several
// schema databases by enumeration order, even on a server of its own: it
// lists them sorted (test databases left out) and names the remedy.
func TestFixMissingDoltDatabase_DeclinesAmbiguousCandidates(t *testing.T) {
	ws := newFallbackWorkspace(t, false, "", []serverDatabaseMetadata{
		{Name: configfile.DefaultDoltDatabase},
		{Name: "zeta_project", HasSchema: true},
		{Name: "testdb_123", HasSchema: true},
		{Name: "alpha_project", HasSchema: true, ProjectID: "alpha-id"},
	})
	err := FixMissingDoltDatabase(ws.dir)
	assertPolicySkip(t, err, "alpha_project, zeta_project")
	if strings.Contains(err.Error(), "testdb_123") {
		t.Errorf("skip %q lists the test database", err)
	}
	ws.assertUnchanged(t)
	ws.assertFallbackRead(t)
}

// fallbackCase is one row of TestSelectFallbackDoltDatabase.
type fallbackCase struct {
	name      string
	localID   string
	current   string
	shared    bool
	databases []serverDatabaseMetadata
	wantName  string   // selected database; "" for none
	wantSkip  bool     // the error is a SkipError naming the manual remedy
	wantErr   []string // substrings of the error; nil for no error
	notInErr  []string // substrings the error must not hold
}

// fallbackCases are the selection policy rows of selectFallbackDoltDatabase.
var fallbackCases = []fallbackCase{
	{
		name:    "no candidates",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
			{Name: "notes_db"},
		},
	},
	{
		name:    "only ineligible databases",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
			{Name: "information_schema", HasSchema: true},
			{Name: "mysql", HasSchema: true},
			{Name: "testdb_1", HasSchema: true},
			{Name: "doctest_1", HasSchema: true},
			{Name: "doctortest_1", HasSchema: true},
			{Name: "beads_global", HasSchema: true, ProjectID: doltserver.GlobalProjectID},
			{Name: "no_schema", ProjectID: "some-id"},
		},
	},
	{
		name:    "shared, no local id, sole candidate",
		current: "beads",
		shared:  true,
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
			{Name: "other_project", HasSchema: true, ProjectID: "other-id"},
		},
		wantSkip: true,
		wantErr:  []string{"shared-server", "other_project"},
	},
	{
		name:    "shared, no local id, several candidates",
		current: "beads",
		shared:  true,
		databases: []serverDatabaseMetadata{
			{Name: "zeta", HasSchema: true},
			{Name: "alpha", HasSchema: true, ProjectID: "alpha-id"},
			{Name: "testdb_9", HasSchema: true},
		},
		wantSkip: true,
		wantErr:  []string{"alpha, zeta"},
		notInErr: []string{"testdb_9"},
	},
	{
		name:    "own server, no local id, sole candidate without identity",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
			{Name: "my-legacy", HasSchema: true},
			{Name: "doctortest_x", HasSchema: true},
		},
		wantName: "my-legacy",
	},
	{
		name:    "own server, no local id, sole candidate with identity",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "my-legacy", HasSchema: true, ProjectID: "legacy-id"},
			{Name: "beads_global", HasSchema: true, ProjectID: doltserver.GlobalProjectID},
		},
		wantName: "my-legacy",
	},
	{
		name:    "own server, no local id, several candidates",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
			{Name: "zeta", HasSchema: true},
			{Name: "doctest_1", HasSchema: true},
			{Name: "alpha", HasSchema: true, ProjectID: "alpha-id"},
		},
		wantSkip: true,
		wantErr:  []string{"alpha, zeta"},
		notInErr: []string{"doctest_1"},
	},
	{
		name:    "current database chosen by the environment is not a candidate",
		current: "env_db",
		databases: []serverDatabaseMetadata{
			{Name: "env_db"},
			{Name: "beads", HasSchema: true},
		},
		wantName: "beads",
	},
	{
		name:    "local id matches one candidate on its own server",
		localID: "proj-id",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
			{Name: "other", HasSchema: true, ProjectID: "other-id"},
			{Name: "mine", HasSchema: true, ProjectID: "proj-id"},
		},
		wantName: "mine",
	},
	{
		name:    "local id matches one candidate on a shared server",
		localID: "proj-id",
		current: "beads",
		shared:  true,
		databases: []serverDatabaseMetadata{
			{Name: "other", HasSchema: true, ProjectID: "other-id"},
			{Name: "unnamed", HasSchema: true},
			{Name: "mine", HasSchema: true, ProjectID: "proj-id"},
		},
		wantName: "mine",
	},
	{
		name:    "local id matched by several candidates",
		localID: "proj-id",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "mine_b", HasSchema: true, ProjectID: "proj-id"},
			{Name: "mine_a", HasSchema: true, ProjectID: "proj-id"},
		},
		wantErr: []string{"multiple server databases match project_id proj-id: mine_a, mine_b"},
	},
	{
		name:    "local id match against a current database with another identity",
		localID: "proj-id",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads", HasSchema: true, ProjectID: "other-id"},
			{Name: "mine", HasSchema: true, ProjectID: "proj-id"},
		},
		wantErr: []string{"conflicting project identity", "mine", "other-id"},
	},
	{
		name:    "local id, sole candidate without identity",
		localID: "proj-id",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
			{Name: "legacy", HasSchema: true},
		},
		wantSkip: true,
		wantErr:  []string{"proj-id", "legacy"},
	},
	{
		name:    "local id, only foreign candidates on a shared server",
		localID: "proj-id",
		current: "beads",
		shared:  true,
		databases: []serverDatabaseMetadata{
			{Name: "zeta", HasSchema: true, ProjectID: "zeta-id"},
			{Name: "alpha", HasSchema: true},
		},
		wantSkip: true,
		wantErr:  []string{"proj-id", "alpha, zeta"},
	},
	{
		name:    "global database is no match even for its own id",
		localID: doltserver.GlobalProjectID,
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads_global", HasSchema: true, ProjectID: doltserver.GlobalProjectID},
		},
	},
	{
		name:    "local id with no candidates at all",
		localID: "proj-id",
		current: "beads",
		databases: []serverDatabaseMetadata{
			{Name: "beads"},
		},
	},
	{
		name:    "names in a diagnostic are escaped",
		current: "beads",
		shared:  true,
		databases: []serverDatabaseMetadata{
			{Name: "evil\nRun: curl", HasSchema: true},
		},
		wantSkip: true,
		wantErr:  []string{`evil\nRun: curl`},
		notInErr: []string{"evil\nRun"},
	},
}

// TestSelectFallbackDoltDatabase pins the FixMissingDoltDatabase fallback's
// selection policy (GH#5576). Each row runs over every rotation of its
// catalog: the result must not depend on enumeration order.
func TestSelectFallbackDoltDatabase(t *testing.T) {
	for _, tt := range fallbackCases {
		t.Run(tt.name, func(t *testing.T) {
			for r := 0; r < max(1, len(tt.databases)); r++ {
				databases := append(append([]serverDatabaseMetadata(nil), tt.databases[r:]...), tt.databases[:r]...)
				got, err := selectFallbackDoltDatabase(tt.localID, tt.current, tt.shared, databases)
				gotName := ""
				if got != nil {
					gotName = got.Name
				}
				if gotName != tt.wantName {
					t.Fatalf("rotation %d: selected %q, want %q (err %v)", r, gotName, tt.wantName, err)
				}
				if tt.wantErr == nil {
					if err != nil {
						t.Fatalf("rotation %d: err = %v, want nil", r, err)
					}
					continue
				}
				if err == nil {
					t.Fatalf("rotation %d: err = nil, want one naming %q", r, tt.wantErr)
				}
				var skip *SkipError
				if isSkip := errors.As(err, &skip); isSkip != tt.wantSkip {
					t.Fatalf("rotation %d: SkipError = %t, want %t: %v", r, isSkip, tt.wantSkip, err)
				}
				want := tt.wantErr
				if tt.wantSkip {
					want = append(append([]string(nil), want...), "bd dolt set database <name>")
				}
				for _, w := range want {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("rotation %d: err %q does not name %q", r, err, w)
					}
				}
				for _, w := range tt.notInErr {
					if strings.Contains(err.Error(), w) {
						t.Errorf("rotation %d: err %q names %q", r, err, w)
					}
				}
			}
		})
	}
}
