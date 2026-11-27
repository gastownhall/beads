package dolt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// userWorkspace lays down a minimal .beads dir whose config.yaml sets
// dolt.user (when non-empty) and returns its path.
func userWorkspace(t *testing.T, yamlUser string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if yamlUser != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
			[]byte("dolt.user: "+yamlUser+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestResolveServerUserForWorkspaceScopesToTarget pins the GH#8577 review
// blocker: a routed or cross-workspace open must resolve dolt.user against
// the workspace being opened, never the launching workspace's merged yaml.
// Real shape: several dolt servers in one fleet, a shared committed
// config.yaml per repo, and personal/test overrides in the uncommitted
// config.local.yaml of the workspace the operator happens to stand in.
func TestResolveServerUserForWorkspaceScopesToTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DOLT_SERVER_USER", "")
	t.Setenv("BD_DOLT_USER", "")

	launcher := userWorkspace(t, "launcher_user")
	target := userWorkspace(t, "") // metadata-only workspace
	targetCfg := &configfile.Config{DoltServerUser: "target_meta_user"}

	// Process config is initialized for the LAUNCHER, as in a routed open.
	t.Setenv("BEADS_DIR", launcher)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("Initialize(launcher): %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	// The launcher's yaml user must not answer for the target: the target's
	// own (absent) yaml falls through to its metadata.
	if got := ResolveServerUserForWorkspace(target, targetCfg); got != "target_meta_user" {
		t.Errorf("cross-workspace: ResolveServerUserForWorkspace() = %q, want target_meta_user", got)
	}

	// The target's own yaml layer outranks its metadata.
	targetYaml := userWorkspace(t, "target_yaml_user")
	if got := ResolveServerUserForWorkspace(targetYaml, targetCfg); got != "target_yaml_user" {
		t.Errorf("target yaml: ResolveServerUserForWorkspace() = %q, want target_yaml_user", got)
	}

	// Same-workspace open keeps the established order (yaml over metadata).
	if got := ResolveServerUserForWorkspace(launcher, &configfile.Config{}); got != "launcher_user" {
		t.Errorf("same workspace: ResolveServerUserForWorkspace() = %q, want launcher_user", got)
	}

	// Env still wins everywhere.
	t.Setenv("BEADS_DOLT_SERVER_USER", "env_user")
	if got := ResolveServerUserForWorkspace(target, targetCfg); got != "env_user" {
		t.Errorf("env: ResolveServerUserForWorkspace() = %q, want env_user", got)
	}

	// WithBeadsDir restored the launcher's context: the global getter still
	// answers launcher_user after the scoped resolutions above.
	if got := (&configfile.Config{}).GetDoltServerUser(); got != "env_user" {
		t.Errorf("post-restore (env set): GetDoltServerUser() = %q, want env_user", got)
	}
	t.Setenv("BEADS_DOLT_SERVER_USER", "")
	if got := (&configfile.Config{}).GetDoltServerUser(); got != "launcher_user" {
		t.Errorf("post-restore: GetDoltServerUser() = %q, want launcher_user (launcher context restored)", got)
	}
}
