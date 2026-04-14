package fix

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestDatabaseIntegrity_ServerModeFailsClosedBeforeBackup(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 49617,
		DoltDatabase:   "beads_AI",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	err := DatabaseIntegrity(dir)
	if err == nil {
		t.Fatal("expected server-mode integrity recovery to fail closed")
	}
	if strings.Contains(err.Error(), ErrTestBinary.Error()) {
		t.Fatalf("expected server-mode guard before bd binary lookup, got %v", err)
	}
	if !strings.Contains(err.Error(), "automatic integrity recovery is disabled for server-mode repos") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "beads_AI") {
		t.Fatalf("error %q does not mention configured database", err)
	}

	if _, statErr := os.Stat(doltDir); statErr != nil {
		t.Fatalf("expected dolt dir to remain in place, got %v", statErr)
	}
	backupPaths, globErr := filepath.Glob(doltDir + ".*.corrupt.backup")
	if globErr != nil {
		t.Fatalf("glob backup paths: %v", globErr)
	}
	if len(backupPaths) != 0 {
		t.Fatalf("expected no corrupt backup path, got %v", backupPaths)
	}
}

func TestDatabaseIntegrity_NonServerModeRestoresRootOnTestBinaryError(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	err := DatabaseIntegrity(dir)
	if !errors.Is(err, ErrTestBinary) {
		t.Fatalf("expected ErrTestBinary, got %v", err)
	}

	if _, statErr := os.Stat(doltDir); statErr != nil {
		t.Fatalf("expected dolt dir to be restored, got %v", statErr)
	}
	backupPaths, globErr := filepath.Glob(doltDir + ".*.corrupt.backup")
	if globErr != nil {
		t.Fatalf("glob backup paths: %v", globErr)
	}
	if len(backupPaths) != 0 {
		t.Fatalf("expected no leftover corrupt backup path, got %v", backupPaths)
	}
}

func TestDatabaseIntegrity_SharedWorktreeServerModeFailsClosedBeforeBackup(t *testing.T) {
	mainRepoDir, worktreeDir := setupSharedWorktreeWorkspace(t)
	sharedBeadsDir := filepath.Join(mainRepoDir, ".beads")
	sharedDoltDir := filepath.Join(sharedBeadsDir, "dolt")
	if err := os.MkdirAll(sharedDoltDir, 0o755); err != nil {
		t.Fatalf("failed to create shared dolt dir: %v", err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 49617,
		DoltDatabase:   "shared_beads",
	}
	if err := cfg.Save(sharedBeadsDir); err != nil {
		t.Fatalf("failed to save shared config: %v", err)
	}

	err := DatabaseIntegrity(worktreeDir)
	if err == nil {
		t.Fatal("expected server-mode integrity recovery to fail closed")
	}
	if !strings.Contains(err.Error(), "shared_beads") {
		t.Fatalf("error %q does not mention shared database", err)
	}

	if _, statErr := os.Stat(sharedDoltDir); statErr != nil {
		t.Fatalf("expected shared dolt dir to remain in place, got %v", statErr)
	}
	if _, err := os.Stat(filepath.Join(worktreeDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected no worktree-local .beads directory, got err=%v", err)
	}
}

func TestDatabaseIntegrity_SharedWorktreeRestoresSharedRootOnTestBinaryError(t *testing.T) {
	mainRepoDir, worktreeDir := setupSharedWorktreeWorkspace(t)
	sharedBeadsDir := filepath.Join(mainRepoDir, ".beads")
	sharedDoltDir := filepath.Join(sharedBeadsDir, "dolt")
	if err := os.MkdirAll(sharedDoltDir, 0o755); err != nil {
		t.Fatalf("failed to create shared dolt dir: %v", err)
	}

	err := DatabaseIntegrity(worktreeDir)
	if !errors.Is(err, ErrTestBinary) {
		t.Fatalf("expected ErrTestBinary, got %v", err)
	}

	if _, statErr := os.Stat(sharedDoltDir); statErr != nil {
		t.Fatalf("expected shared dolt dir to be restored, got %v", statErr)
	}
	backupPaths, globErr := filepath.Glob(sharedDoltDir + ".*.corrupt.backup")
	if globErr != nil {
		t.Fatalf("glob backup paths: %v", globErr)
	}
	if len(backupPaths) != 0 {
		t.Fatalf("expected no leftover corrupt backup path, got %v", backupPaths)
	}
	if _, err := os.Stat(filepath.Join(worktreeDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected no worktree-local .beads directory, got err=%v", err)
	}
}
