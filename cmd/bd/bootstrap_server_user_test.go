package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestInitServerCloneEndpointScopesUserToWorkspace pins the GH#8577 review F1
// fix for the bootstrap/init path: initServerCloneEndpoint resolves the dial
// endpoint under config.WithBeadsDir(beadsDir) — the same scoping its sibling
// bootstrapServerEndpoint already used — so the init-target workspace's own
// layers answer, never the launching workspace's personal yaml dolt.user.
func TestInitServerCloneEndpointScopesUserToWorkspace(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_USER", "")
	t.Setenv("BD_DOLT_USER", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

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

	targetDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetCfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltDatabase:   "beads_test",
		DoltServerUser: "target_user",
		DoltServerPort: 12345,
	}
	meta, err := json.Marshal(targetCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "metadata.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}

	// The endpoint may come back with an unresolved-port error in a bare temp
	// workspace; the user resolution is what this test pins, and ep is
	// populated on the error paths too.
	ep, _ := initServerCloneEndpoint(targetDir, targetCfg)
	if ep.user != "target_user" {
		t.Errorf("initServerCloneEndpoint user = %q, want target_user "+
			"(scoped to the init-target workspace, not the launcher's yaml)", ep.user)
	}
}
