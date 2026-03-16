package beads

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

func TestResolveRepoRuntimeFromBeadsDir_CustomDataDirAndDatabase(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	externalDoltDir := filepath.Join(repoDir, "external-dolt")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(externalDoltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Database:       "dolt",
		DoltDatabase:   "beads_custom",
		DoltDataDir:    "../external-dolt",
		DoltServerHost: "127.0.0.1",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("14123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := ResolveRepoRuntimeFromBeadsDir(beadsDir)
	if err != nil {
		t.Fatalf("ResolveRepoRuntimeFromBeadsDir() error = %v", err)
	}

	if !utils.PathsEqual(runtime.BeadsDir, beadsDir) {
		t.Fatalf("BeadsDir = %q, want %q", runtime.BeadsDir, beadsDir)
	}
	if !utils.PathsEqual(runtime.DatabasePath, externalDoltDir) {
		t.Fatalf("DatabasePath = %q, want %q", runtime.DatabasePath, externalDoltDir)
	}
	if runtime.Database != "beads_custom" {
		t.Fatalf("Database = %q, want beads_custom", runtime.Database)
	}
	if runtime.Port != 14123 {
		t.Fatalf("Port = %d, want 14123", runtime.Port)
	}
	if runtime.OwnershipMode != RuntimeOwnershipRepoManaged {
		t.Fatalf("OwnershipMode = %q, want %q", runtime.OwnershipMode, RuntimeOwnershipRepoManaged)
	}
}

func TestResolveRepoRuntimeFromDBPath_CustomDataDir(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(originalWD) }()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	externalDoltDir := filepath.Join(repoDir, "external-dolt")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(externalDoltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "beads_custom",
		DoltDataDir:  "../external-dolt",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	runtime, err := ResolveRepoRuntimeFromDBPath(externalDoltDir)
	if err != nil {
		t.Fatalf("ResolveRepoRuntimeFromDBPath() error = %v", err)
	}

	if !utils.PathsEqual(runtime.BeadsDir, beadsDir) {
		t.Fatalf("BeadsDir = %q, want %q", runtime.BeadsDir, beadsDir)
	}
	if !utils.PathsEqual(runtime.DatabasePath, externalDoltDir) {
		t.Fatalf("DatabasePath = %q, want %q", runtime.DatabasePath, externalDoltDir)
	}
	if runtime.Database != "beads_custom" {
		t.Fatalf("Database = %q, want beads_custom", runtime.Database)
	}
}

func TestResolveRepoRuntimeFromRepoPath_UsesWorktreeFallback(t *testing.T) {
	bareDir, featureWorktreeDir := setupBareParentWorktree(t)
	fallbackBeadsDir := filepath.Join(bareDir, ".beads")
	if err := os.MkdirAll(fallbackBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "beads_worktree",
	}
	if err := cfg.Save(fallbackBeadsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fallbackBeadsDir, "dolt-server.port"), []byte("15123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := ResolveRepoRuntimeFromRepoPath(featureWorktreeDir)
	if err != nil {
		t.Fatalf("ResolveRepoRuntimeFromRepoPath() error = %v", err)
	}

	if !utils.PathsEqual(runtime.BeadsDir, fallbackBeadsDir) {
		t.Fatalf("BeadsDir = %q, want %q", runtime.BeadsDir, fallbackBeadsDir)
	}
	if runtime.Database != "beads_worktree" {
		t.Fatalf("Database = %q, want beads_worktree", runtime.Database)
	}
	if runtime.Port != 15123 {
		t.Fatalf("Port = %d, want 15123", runtime.Port)
	}
}

func TestResolveRepoRuntimeFromBeadsDir_PreservesSourceDatabaseAcrossRedirect(t *testing.T) {
	repoDir := t.TempDir()
	sourceBeadsDir := filepath.Join(repoDir, "project", ".beads")
	targetBeadsDir := filepath.Join(repoDir, "shared", ".beads")

	if err := os.MkdirAll(sourceBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(targetBeadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	sourceCfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "source_db",
	}
	targetCfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "target_db",
	}
	if err := sourceCfg.Save(sourceBeadsDir); err != nil {
		t.Fatal(err)
	}
	if err := targetCfg.Save(targetBeadsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceBeadsDir, RedirectFileName), []byte("../shared/.beads\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := ResolveRepoRuntimeFromBeadsDir(sourceBeadsDir)
	if err != nil {
		t.Fatalf("ResolveRepoRuntimeFromBeadsDir() error = %v", err)
	}

	if !runtime.Redirect.WasRedirected {
		t.Fatal("expected redirect to be followed")
	}
	if !utils.PathsEqual(runtime.BeadsDir, targetBeadsDir) {
		t.Fatalf("BeadsDir = %q, want %q", runtime.BeadsDir, targetBeadsDir)
	}
	if !utils.PathsEqual(runtime.SourceBeadsDir, sourceBeadsDir) {
		t.Fatalf("SourceBeadsDir = %q, want %q", runtime.SourceBeadsDir, sourceBeadsDir)
	}
	if runtime.Database != "source_db" {
		t.Fatalf("Database = %q, want source_db", runtime.Database)
	}
}
