package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestDoltServerConfigScopesUserToWorkspace pins the GH#8577 review F1 fix:
// doltServerConfig targets a FOREIGN workspace, so the launcher's merged yaml
// (a personal config.local.yaml dolt.user override) must not answer for the
// federation check against that target — the target's own layers and metadata
// must. Pre-fix, the launcher's user authenticated against every foreign
// database the doctor scanned.
func TestDoltServerConfigScopesUserToWorkspace(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_USER", "")
	t.Setenv("BD_DOLT_USER", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Launcher workspace with a personal yaml user, as the process-global
	// config a routed doctor run would carry.
	launcherDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(launcherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launcherDir, "config.yaml"),
		[]byte("dolt.user: launcher_user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", launcherDir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("Initialize(launcher): %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	// Foreign target: metadata-only workspace (the committed fleet shape).
	targetDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltDatabase:   "beads_test",
		DoltServerUser: "target_user",
		DoltServerPort: 12345,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "metadata.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := doltServerConfig(targetDir, filepath.Join(targetDir, "dolt"))
	if cfg.ServerUser != "target_user" {
		t.Errorf("doltServerConfig ServerUser = %q, want target_user "+
			"(launcher yaml must not leak into a foreign workspace)", cfg.ServerUser)
	}
}
