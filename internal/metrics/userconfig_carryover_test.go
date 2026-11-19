package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// darwinUserConfigSeam simulates a macOS host from any OS: setupUserConfigHome
// isolates HOME/XDG, and the config package's exported platform seam points the
// native resolver at a stand-in ~/Library/Application Support directory.
func darwinUserConfigSeam(t *testing.T) (home, nativeDir string) {
	t.Helper()
	home = setupUserConfigHome(t)
	nativeDir = filepath.Join(home, "Library", "Application Support")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatalf("mkdir application support stand-in: %v", err)
	}
	restore := config.SetUserConfigPlatformForTest("darwin", func() (string, error) {
		return nativeDir, nil
	})
	t.Cleanup(restore)
	return home, nativeDir
}

// TestEnsureUserConfigDefaults_DarwinCarriesOverLegacyConsent is the GH#8113
// bootstrap contract: the first command on a mac with only a legacy
// ~/Library/Application Support config creates ~/.config/bd/config.yaml with
// the legacy bytes (metrics off stays off — which is what metrics.Init's
// enable argument resolves to on that same run, and what suppresses the
// first-run notice), and the second command never re-bootstraps.
func TestEnsureUserConfigDefaults_DarwinCarriesOverLegacyConsent(t *testing.T) {
	home, nativeDir := darwinUserConfigSeam(t)
	legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	legacy := "metrics:\n  disabled: true\nnode_id: n1\n"
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("plant legacy config: %v", err)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	path := userConfigPath(home)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	if string(got) != legacy {
		t.Errorf("created config = %q, want legacy bytes %q verbatim", got, legacy)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("created config mode = %o, want 600", perm)
	}

	// metrics.Init's enable argument and the first-run notice gate both
	// resolve through the consent reader; the carried opt-out governs them.
	if !config.MetricsDisabledByUserConfig() {
		t.Error("MetricsDisabledByUserConfig() = false; carried opt-out lost on the carry-over run")
	}

	// The next command: existing-file logic only; disabled stays true and
	// the missing endpoint leaf is filled.
	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("second EnsureUserConfigDefaults: %v", err)
	}
	parsed := parseUserConfig(t, path)
	if got := metricsLeaf(t, parsed, "disabled"); got != true {
		t.Errorf("metrics.disabled = %v, want true", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != DefaultEndpoint {
		t.Errorf("metrics.endpoint = %v, want %q", got, DefaultEndpoint)
	}
	if got, err := os.ReadFile(legacyPath); err != nil || string(got) != legacy {
		t.Errorf("legacy config was disturbed: %q, %v", got, err)
	}
}
