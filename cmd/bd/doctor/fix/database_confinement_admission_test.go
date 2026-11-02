package fix

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestDatabaseIntegrityAdmitsSupportedSelections covers AC-7: a normal
// workspace, a supported symlinked .beads workspace alias, and an actual
// explicitly env-selected data directory stay admitted by the confinement,
// so the recovery proceeds to the (test-binary) reinitialization and the
// data survives the round trip.
func TestDatabaseIntegrityAdmitsSupportedSelections(t *testing.T) {
	t.Run("normal workspace", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_DATA_DIR", "")
		dir := setupTestWorkspace(t)
		doltDir := filepath.Join(dir, ".beads", "dolt")
		if err := os.MkdirAll(doltDir, 0o755); err != nil {
			t.Fatal(err)
		}

		err := DatabaseIntegrity(dir)
		if !errors.Is(err, ErrTestBinary) {
			t.Fatalf("expected the normal workspace to pass confinement and reach the test-binary guard, got %v", err)
		}
		if _, statErr := os.Stat(doltDir); statErr != nil {
			t.Errorf("normal database not restored: %v", statErr)
		}
	})

	t.Run("symlinked .beads workspace alias", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_DATA_DIR", "")
		root := t.TempDir()
		dir := filepath.Join(root, "repo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		store := filepath.Join(root, "real-store")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := (&configfile.Config{Backend: configfile.BackendDolt}).Save(store); err != nil {
			t.Fatalf("save config: %v", err)
		}
		doltDir := filepath.Join(store, "dolt")
		if err := os.MkdirAll(doltDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// .beads itself a symlink is a documented layout (REPO_CONTEXT), so
		// the confinement resolves the workspace root instead of refusing it
		// (#7889's blanket refusal is deliberately inverted).
		if err := os.Symlink(store, filepath.Join(dir, ".beads")); err != nil {
			t.Fatal(err)
		}

		err := DatabaseIntegrity(dir)
		if !errors.Is(err, ErrTestBinary) {
			t.Fatalf("expected the symlinked workspace alias to pass confinement and reach the test-binary guard, got %v", err)
		}
		if _, statErr := os.Stat(doltDir); statErr != nil {
			t.Errorf("aliased database not restored: %v", statErr)
		}
	})

	t.Run("explicitly env-selected data dir", func(t *testing.T) {
		dir := setupTestWorkspace(t)
		beadsDir := filepath.Join(dir, ".beads")
		// The env override only reaches DatabasePath through a loaded config;
		// without one, getDatabasePath falls back to .beads/dolt.
		if err := (&configfile.Config{Backend: configfile.BackendDolt}).Save(beadsDir); err != nil {
			t.Fatalf("save config: %v", err)
		}
		envDir := filepath.Join(dir, "env-dolt")
		if err := os.MkdirAll(envDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_DATA_DIR", envDir)

		err := DatabaseIntegrity(dir)
		if !errors.Is(err, ErrTestBinary) {
			t.Fatalf("expected the env-selected database to pass confinement and reach the test-binary guard, got %v", err)
		}
		if _, statErr := os.Stat(envDir); statErr != nil {
			t.Errorf("env-selected database not restored: %v", statErr)
		}
	})
}

// TestRepoFingerprintChoice2ReachesConfirmationForSafeSelection covers AC-7
// for the fingerprint reinitialize path: a confined-admissible database
// selection still gets as far as the DELETE confirmation, where a negative
// answer cancels without error and without touching the database.
func TestRepoFingerprintChoice2ReachesConfirmationForSafeSelection(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	dir := setupTestWorkspace(t)
	doltDir := filepath.Join(dir, ".beads", "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	reads := 0
	oldReadLine := repoFingerprintReadLine
	defer func() { repoFingerprintReadLine = oldReadLine }()
	repoFingerprintReadLine = func() (string, error) {
		reads++
		if reads == 1 {
			return "2", nil
		}
		return "n", nil
	}

	if err := RepoFingerprint(dir, false); err != nil {
		t.Fatalf("expected a safe selection to reach the confirmation and cancel cleanly, got %v", err)
	}
	if reads != 2 {
		t.Fatalf("expected the choice and the confirmation reads, got %d", reads)
	}
	if _, statErr := os.Stat(doltDir); statErr != nil {
		t.Errorf("database touched by a canceled reinitialization: %v", statErr)
	}
}

// TestDatabaseIntegrityServerModeGuardPrecedesConfinement covers AC-7's
// precedence rule: the existing server-mode refusal stays first, so a
// server-mode workspace is rejected by that guard even when its metadata
// also selects an unconfined database path.
func TestDatabaseIntegrityServerModeGuardPrecedesConfinement(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 49617,
		DoltDatabase:   "beads_AI",
		DoltDataDir:    "../outside-dolt",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	err := DatabaseIntegrity(dir)
	if err == nil {
		t.Fatal("expected server-mode integrity recovery to fail closed")
	}
	if !strings.Contains(err.Error(), "automatic integrity recovery is disabled for server-mode repos") {
		t.Fatalf("expected the server-mode guard to fire first, got %v", err)
	}
	if strings.Contains(err.Error(), "outside the workspace") {
		t.Errorf("confinement refusal ran before the server-mode guard: %v", err)
	}
}