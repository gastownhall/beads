package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestEnsureUserConfigDefaults_DarwinUnreadableLegacyFailsClosed: a legacy
// probe that fails with anything but not-exist (a TCC denial) creates the
// config with metrics off, and metrics stay off on later runs until
// "bd metrics on".
func TestEnsureUserConfigDefaults_DarwinUnreadableLegacyFailsClosed(t *testing.T) {
	_, nativeDir := darwinUserConfigSeam(t)
	legacyDir := filepath.Join(nativeDir, "bd")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "config.yaml"), []byte("metrics:\n  disabled: false\n"), 0o600); err != nil {
		t.Fatalf("plant legacy config: %v", err)
	}
	if err := os.Chmod(legacyDir, 0o000); err != nil {
		t.Fatalf("chmod legacy dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacyDir, 0o755) })

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	if !config.MetricsDisabledByUserConfig() {
		t.Fatal("MetricsDisabledByUserConfig() = false; an unreadable legacy opt-out must fail closed")
	}
	// ... and stay off on later runs.
	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("second EnsureUserConfigDefaults: %v", err)
	}
	if !config.MetricsDisabledByUserConfig() {
		t.Fatal("MetricsDisabledByUserConfig() = false on the second run")
	}
}
