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
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
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

// isolateDoltEnvForFallbackTest clears inherited Dolt endpoint, database and
// shared-mode settings and isolates user configuration, so a fallback
// selection test sees only the workspace metadata.json it writes.
func isolateDoltEnvForFallbackTest(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT",
		"BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_SOCKET",
		"BEADS_DOLT_DATA_DIR", "BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_AUTO_START",
		"BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_PASSWORD",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// newRefusingDoltListener holds a loopback listener that accepts connections
// and closes them at once. The fallback tests point the workspace's server
// endpoint at it, so any raw dial (the pre-fix probe's, or a stray one after)
// fails promptly without contacting any real server.
func newRefusingDoltListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// stubServerMetadataCatalog pins the catalog every listServerMetadataDatabases
// call returns and counts the calls. The count lets a test assert its case
// actually reaches the fallback's own catalog read (the second call), rather
// than being answered — or hidden — by the authoritative reconcile's first one.
func stubServerMetadataCatalog(t *testing.T, catalog []serverDatabaseMetadata) *int {
	t.Helper()
	orig := listServerMetadataDatabases
	calls := 0
	listServerMetadataDatabases = func(_ string, _ *configfile.Config) ([]serverDatabaseMetadata, error) {
		calls++
		return catalog, nil
	}
	t.Cleanup(func() { listServerMetadataDatabases = orig })
	return &calls
}

// newFallbackFixWorkspace writes a server-mode metadata.json with an empty
// dolt_database, the local project_id the caller names, and the server
// endpoint at the refusing listener. It returns the workspace dir, its
// .beads dir, and metadata.json's current bytes.
func newFallbackFixWorkspace(t *testing.T, port int, projectID string) (string, string, []byte) {
	t.Helper()
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		ProjectID:      projectID,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	return dir, beadsDir, before
}

// assertMetadataUnchanged fails the test when metadata.json's bytes changed.
func assertMetadataUnchanged(t *testing.T, beadsDir string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("re-read metadata.json: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("metadata.json changed:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestFixMissingDoltDatabase_SharedServerDeclinesUnanchoredCandidate pins
// GH#5576: in shared-server mode, with no local project_id and no
// dolt_database, FixMissingDoltDatabase's schema-only fallback must not adopt
// a sole schema-matching candidate — the exact auto-adoption
// reconcileAuthoritativeServerMetadata refuses as able to redirect this
// workspace to another project's data. It declines with a skip that names the
// candidates and the manual database-setting command, and metadata.json
// stays byte-for-byte unchanged. Pre-fix, the raw probe dialed the server and
// adopted the first schema-matching database; the refusing listener makes
// that path fail as a connection skip without the policy diagnostic.
func TestFixMissingDoltDatabase_SharedServerDeclinesUnanchoredCandidate(t *testing.T) {
	isolateDoltEnvForFallbackTest(t)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	port := newRefusingDoltListener(t)
	dir, beadsDir, before := newFallbackFixWorkspace(t, port, "")

	calls := stubServerMetadataCatalog(t, []serverDatabaseMetadata{
		{Name: configfile.DefaultDoltDatabase, HasSchema: false},
		{Name: "foreign_db", HasSchema: true, ProjectID: "other-project"},
	})

	err := FixMissingDoltDatabase(dir)

	if err == nil {
		t.Fatal("FixMissingDoltDatabase returned nil, want a skip refusing to adopt the sole candidate")
	}
	var skip *SkipError
	if !errors.As(err, &skip) {
		t.Fatalf("FixMissingDoltDatabase error = %v, want a *fix.SkipError", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "foreign_db") {
		t.Errorf("skip does not name the candidate:\n%s", msg)
	}
	if !strings.Contains(msg, "bd dolt set database <name>") {
		t.Errorf("skip does not give the manual remedy:\n%s", msg)
	}
	assertMetadataUnchanged(t, beadsDir, before)
	if *calls != 2 {
		t.Fatalf("catalog read %d times, want 2 (reconcile + fallback)", *calls)
	}
}

// TestFixMissingDoltDatabase_KnownProjectIDWithoutMatchDeclines pins GH#5576:
// a known local project_id with no eligible candidate carrying it declines in
// either shared or non-shared mode — including when the sole candidate has no
// identity at all: absence of identity is not a match.
func TestFixMissingDoltDatabase_KnownProjectIDWithoutMatchDeclines(t *testing.T) {
	for _, mode := range []struct {
		name   string
		shared string
	}{
		{"shared server mode", "1"},
		{"non-shared server mode", ""},
	} {
		t.Run(mode.name, func(t *testing.T) {
			isolateDoltEnvForFallbackTest(t)
			if mode.shared != "" {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", mode.shared)
			}
			port := newRefusingDoltListener(t)
			dir, beadsDir, before := newFallbackFixWorkspace(t, port, "proj-123")

			calls := stubServerMetadataCatalog(t, []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "legacy_db", HasSchema: true, ProjectID: ""},
			})

			err := FixMissingDoltDatabase(dir)

			var skip *SkipError
			if !errors.As(err, &skip) {
				t.Fatalf("FixMissingDoltDatabase error = %v, want a *fix.SkipError", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "proj-123") {
				t.Errorf("skip does not identify the unmatched local project_id:\n%s", msg)
			}
			if !strings.Contains(msg, "legacy_db") {
				t.Errorf("skip does not name the candidates:\n%s", msg)
			}
			if !strings.Contains(msg, "bd dolt set database <name>") {
				t.Errorf("skip does not give the manual remedy:\n%s", msg)
			}
			assertMetadataUnchanged(t, beadsDir, before)
			if *calls != 2 {
				t.Fatalf("catalog read %d times, want 2 (reconcile + fallback)", *calls)
			}
		})
	}
}

// TestFixMissingDoltDatabase_MultipleCandidatesWithoutIdentityDeclinesSorted
// pins GH#5576: with no local project_id on a non-shared server and several
// eligible candidates, the fallback never chooses by enumeration order. It
// declines with a skip listing the candidates sorted, plus the manual remedy,
// and metadata.json stays unchanged.
func TestFixMissingDoltDatabase_MultipleCandidatesWithoutIdentityDeclinesSorted(t *testing.T) {
	isolateDoltEnvForFallbackTest(t)
	port := newRefusingDoltListener(t)
	dir, beadsDir, before := newFallbackFixWorkspace(t, port, "")

	calls := stubServerMetadataCatalog(t, []serverDatabaseMetadata{
		{Name: configfile.DefaultDoltDatabase, HasSchema: false},
		{Name: "zeta_db", HasSchema: true, ProjectID: "zeta"},
		{Name: "alpha_db", HasSchema: true, ProjectID: "alpha"},
	})

	err := FixMissingDoltDatabase(dir)

	var skip *SkipError
	if !errors.As(err, &skip) {
		t.Fatalf("FixMissingDoltDatabase error = %v, want a *fix.SkipError", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "alpha_db, zeta_db") {
		t.Errorf("skip does not list the candidates in sorted order:\n%s", msg)
	}
	if !strings.Contains(msg, "bd dolt set database <name>") {
		t.Errorf("skip does not give the manual remedy:\n%s", msg)
	}
	assertMetadataUnchanged(t, beadsDir, before)
	if *calls != 2 {
		t.Fatalf("catalog read %d times, want 2 (reconcile + fallback)", *calls)
	}
}

// TestFixMissingDoltDatabase_AdoptsSoleLegacyCandidateSkippingTestFixture
// pins the retained GH#2160 recovery: on a non-shared server with no local
// project_id, a single eligible legacy candidate is adopted — while the
// schema-matching test-fixture decoy is excluded from the fallback and (by
// making two schema candidates) keeps the authoritative reconcile from
// adopting first and hiding the fallback. The absent project_id is backfilled
// from the selected candidate's identity.
func TestFixMissingDoltDatabase_AdoptsSoleLegacyCandidateSkippingTestFixture(t *testing.T) {
	isolateDoltEnvForFallbackTest(t)
	port := newRefusingDoltListener(t)
	dir, beadsDir, _ := newFallbackFixWorkspace(t, port, "")

	calls := stubServerMetadataCatalog(t, []serverDatabaseMetadata{
		{Name: configfile.DefaultDoltDatabase, HasSchema: false},
		{Name: "testdb_decoy", HasSchema: true, ProjectID: "decoy"},
		{Name: "my-project", HasSchema: true, ProjectID: "legacy-proj"},
	})

	if err := FixMissingDoltDatabase(dir); err != nil {
		t.Fatalf("FixMissingDoltDatabase failed: %v", err)
	}

	after, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("reload metadata.json: %v", err)
	}
	if after.DoltDatabase != "my-project" {
		t.Fatalf("DoltDatabase = %q, want the legacy candidate %q", after.DoltDatabase, "my-project")
	}
	if after.ProjectID != "legacy-proj" {
		t.Fatalf("ProjectID = %q, want it backfilled from the candidate as %q", after.ProjectID, "legacy-proj")
	}
	if *calls != 2 {
		t.Fatalf("catalog read %d times, want 2 (reconcile + fallback)", *calls)
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
