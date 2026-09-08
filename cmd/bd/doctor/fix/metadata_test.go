package fix

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
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
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()
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
		if err := cmd.Run(); err != nil {
			// git init must succeed; config failures are non-fatal
			if args[1] == "init" {
				t.Fatalf("git init failed: %v", err)
			}
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
		{Name: "wrong_db", HasSchema: true, ProjectID: "other-proj"},
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
		{"access denied (1045)", &mysql.MySQLError{Number: 1045, Message: "Access denied"}, false},
		{"access denied for db (1044)", &mysql.MySQLError{Number: 1044, Message: "Access denied for user"}, false},
		{"generic error", errors.New("connection reset"), false},
		{"wrapped ErrNoRows", fmt.Errorf("wrapped: %w", sql.ErrNoRows), true},
		{"wrapped access denied", fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1045}), false},
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
