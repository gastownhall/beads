package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// TestDiscoveredBackendRefusesUnrecognizedDoltMode pins GH#5104: a workspace
// whose metadata.json names a dolt_mode this build does not know (as
// proxied-server was to bd 1.1.0) must be refused before any store opens,
// not routed to the embedded store, where it reads as an empty tracker.
func TestDiscoveredBackendRefusesUnrecognizedDoltMode(t *testing.T) {
	beadsDir := t.TempDir()
	meta := `{"backend":"dolt","dolt_mode":"some-future-mode","dolt_database":"beads"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := checkDiscoveredBackend(beadsDir)
	if err == nil {
		t.Fatalf("checkDiscoveredBackend(dolt_mode=some-future-mode) = nil error; selectsEmbeddedStore=%v: an unrecognized mode is admitted and falls through to the embedded store instead of being refused",
			selectsEmbeddedStore(beadsDir, cfg))
	}
}

// writeLegacyUnknownModeConfig writes a workspace whose only metadata is a
// legacy config.json naming an unknown dolt_mode beside a Dolt data dir, and
// returns the config.json bytes.
func writeLegacyUnknownModeConfig(t *testing.T, beadsDir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	legacy := `{"backend":"dolt","dolt_mode":"some-future-mode","dolt_database":"beads_legacy","future_binding":"keep-me"}`
	if err := os.WriteFile(filepath.Join(beadsDir, configfile.LegacyConfigFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	return legacy
}

// assertLegacyUnknownModeUntouched fails unless the refusal err names the
// unknown mode and config.json, and the workspace still holds exactly the
// byte-identical config.json and the Dolt dir: Load's legacy migration (which
// writes metadata.json, drops unknown keys and deletes config.json) never ran.
func assertLegacyUnknownModeUntouched(t *testing.T, beadsDir, legacy, refusal string) {
	t.Helper()
	entries, err := os.ReadDir(beadsDir)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if err != nil || !slices.Equal(names, []string{configfile.LegacyConfigFileName, "dolt"}) {
		t.Fatalf("refusal changed the workspace: %v, %v", names, err)
	}
	if got, err := os.ReadFile(filepath.Join(beadsDir, configfile.LegacyConfigFileName)); err != nil || string(got) != legacy {
		t.Fatalf("refusal rewrote config.json: %q, %v", got, err)
	}
	if !strings.Contains(refusal, `dolt_mode "some-future-mode"`) || !strings.Contains(refusal, configfile.LegacyConfigFileName) || !strings.Contains(refusal, configfile.BackendNotOpenedGuarantee) {
		t.Fatalf("refusal = %q, want the unknown-dolt_mode refusal naming config.json", refusal)
	}
}

// TestUnknownDoltModeInLegacyConfigRefusedBeforeMigration pins that an unknown
// dolt_mode in a not-yet-migrated config.json is refused by command admission
// and by every store factory (including the routed and non-mutating ones,
// which open workspaces admission never checked) before Load migrates it.
func TestUnknownDoltModeInLegacyConfigRefusedBeforeMigration(t *testing.T) {
	t.Run("command admission", func(t *testing.T) {
		bd := buildBDForInitTests(t)
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		legacy := writeLegacyUnknownModeConfig(t, beadsDir)
		cmd := exec.Command(bd, "list", "--json")
		cmd.Dir = root
		cmd.Env = append(removedBackendTestEnv(beadsDir), "BEADS_DOLT_AUTO_START=0", "BD_DISABLE_METRICS=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("bd list admitted a legacy config.json with an unknown dolt_mode:\n%s", out)
		}
		assertLegacyUnknownModeUntouched(t, beadsDir, legacy, string(out))
	})

	factories := map[string]func(context.Context, string) (storage.DoltStorage, error){
		"read-write": newDoltStoreFromConfig,
		"routed":     newExistingDoltStoreFromConfig,
		"read-only":  newReadOnlyStoreFromConfig,
		"preview":    newPreviewStoreFromConfig,
	}
	for name, open := range factories {
		t.Run("factory/"+name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			legacy := writeLegacyUnknownModeConfig(t, beadsDir)
			store, err := open(t.Context(), beadsDir)
			if err == nil {
				_ = store.Close()
				t.Fatalf("%s factory opened a workspace whose config.json names an unknown dolt_mode", name)
			}
			assertLegacyUnknownModeUntouched(t, beadsDir, legacy, err.Error())
		})
	}
}
