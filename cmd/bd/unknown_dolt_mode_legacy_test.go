package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// TestUnknownDoltModeLegacyConfigRefusedBeforeMigration pins that an unknown
// dolt_mode written only in a legacy config.json is refused before Load
// migrates that file into metadata.json (dropping the keys Config does not
// know and deleting config.json). Command admission, the legacy upgrade
// guard, and every store factory — including the routed and non-mutating
// ones, which also open workspaces bd was not launched in — must refuse with
// the same text, naming config.json, and leave the workspace byte-identical.
func TestUnknownDoltModeLegacyConfigRefusedBeforeMigration(t *testing.T) {
	writeLegacyUnknownModeConfig := func(t *testing.T) (string, string) {
		t.Helper()
		beadsDir := t.TempDir()
		legacy := `{"backend":"dolt","dolt_mode":"future-mode","dolt_database":"beads_legacy","future_binding":"keep-me"}`
		if err := os.WriteFile(filepath.Join(beadsDir, configfile.LegacyConfigFileName), []byte(legacy), 0o600); err != nil {
			t.Fatalf("write legacy config.json: %v", err)
		}
		return beadsDir, legacy
	}
	refusedInPlace := func(t *testing.T, beadsDir, legacy, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s admitted a workspace whose config.json names unknown dolt_mode %q", name, "future-mode")
		}
		if !strings.Contains(err.Error(), "dolt_mode") || !strings.Contains(err.Error(), "future-mode") ||
			!strings.Contains(err.Error(), configfile.BackendNotOpenedGuarantee) {
			t.Fatalf("%s error = %v, want the unknown-dolt_mode refusal with the data-safety guarantee", name, err)
		}
		if !strings.Contains(err.Error(), "config.json") || strings.Contains(err.Error(), "metadata.json") {
			t.Fatalf("%s error = %v, want it to name config.json and not a metadata.json that does not exist", name, err)
		}
		if got, readErr := os.ReadFile(filepath.Join(beadsDir, configfile.LegacyConfigFileName)); readErr != nil || string(got) != legacy {
			t.Fatalf("%s rewrote config.json: %q, %v", name, got, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, configfile.ConfigFileName)); !os.IsNotExist(statErr) {
			t.Fatalf("%s migrated config.json into metadata.json (stat error: %v)", name, statErr)
		}
	}

	t.Run("command admission", func(t *testing.T) {
		beadsDir, legacy := writeLegacyUnknownModeConfig(t)
		want, err := checkDiscoveredBackend(beadsDir)
		if err == nil {
			t.Fatalf("checkDiscoveredBackend admitted unknown dolt_mode; selectsEmbeddedStore=%v",
				selectsEmbeddedStore(beadsDir, want))
		}
		refusedInPlace(t, beadsDir, legacy, "checkDiscoveredBackend", err)
		// The command-admission seam (bootstrap, guardLegacyUpgradeWorkspace)
		// refuses with the same text, in the same order.
		if got := guardLegacyUpgradeWorkspace(beadsDir); got == nil || got.Error() != err.Error() {
			t.Fatalf("guardLegacyUpgradeWorkspace error = %v, want admission's own refusal %q", got, err)
		}
	})

	factories := map[string]func(context.Context, string) (storage.DoltStorage, error){
		"read-write": newDoltStoreFromConfig,
		"routed":     newExistingDoltStoreFromConfig,
		"read-only":  newReadOnlyStoreFromConfig,
		"preview":    newPreviewStoreFromConfig,
	}
	for name, open := range factories {
		t.Run(name, func(t *testing.T) {
			beadsDir, legacy := writeLegacyUnknownModeConfig(t)
			store, err := open(t.Context(), beadsDir)
			if err == nil {
				_ = store.Close()
				t.Fatalf("%s factory opened a workspace whose config.json names an unknown dolt_mode", name)
			}
			refusedInPlace(t, beadsDir, legacy, name+" factory", err)
		})
	}

	t.Run("bd list refuses before migration", func(t *testing.T) {
		bd := buildBDForInitTests(t)
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatalf("create .beads: %v", err)
		}
		legacy := `{"backend":"dolt","dolt_mode":"future-mode","dolt_database":"beads_legacy","future_binding":"keep-me"}`
		if err := os.WriteFile(filepath.Join(beadsDir, configfile.LegacyConfigFileName), []byte(legacy), 0o600); err != nil {
			t.Fatalf("write legacy config.json: %v", err)
		}
		cmd := exec.Command(bd, "list", "--json")
		cmd.Dir = root
		cmd.Env = append(removedBackendTestEnv(beadsDir), "BD_DISABLE_METRICS=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("bd list unexpectedly opened a legacy config.json workspace with unknown dolt_mode:\n%s", out)
		}
		message := string(out)
		if !strings.Contains(message, "dolt_mode") || !strings.Contains(message, "future-mode") {
			t.Errorf("bd list unknown-mode error missing the field and value:\n%s", out)
		}
		if !strings.Contains(message, "config.json") || strings.Contains(message, "metadata.json") {
			t.Errorf("bd list unknown-mode error must name config.json and not a metadata.json that does not exist:\n%s", out)
		}
		if got, readErr := os.ReadFile(filepath.Join(beadsDir, configfile.LegacyConfigFileName)); readErr != nil || string(got) != legacy {
			t.Fatalf("bd list rewrote config.json: %q, %v", got, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, configfile.ConfigFileName)); !os.IsNotExist(statErr) {
			t.Fatalf("bd list migrated config.json into metadata.json (stat error: %v)", statErr)
		}
	})
}
