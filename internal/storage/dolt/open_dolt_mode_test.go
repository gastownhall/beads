package dolt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// The endpoint env pins force every open that gets past the mode refusal to
// fail on the dial to 127.0.0.1:1, fast and without spawning anything. An
// open that reports the mode refusal instead proves the refusal runs before
// metadata migration, endpoint resolution, dialing and auto-start; an open
// that reports a connection error means the mode slipped through (GH#5104).
func unknownDoltModeTestEndpoint(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
}

func wantUnknownDoltModeRefusal(t *testing.T, where, beadsDir string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s opened a workspace whose metadata names unknown dolt_mode %q", where, "future-mode")
	}
	for _, want := range []string{
		"dolt_mode", "future-mode", "not recognized", configfile.BackendNotOpenedGuarantee,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s error missing %q: %v", where, want, err)
		}
	}
	// The endpoint this test pins fails with a connection error, so these
	// never appear when the refusal ran first. (Beware substring matches on
	// the failure text: the test's own name lands in the temp-dir path the
	// refusal quotes.)
	for _, notWant := range []string{"connection refused", "failed to connect"} {
		if strings.Contains(err.Error(), notWant) {
			t.Errorf("%s reached the endpoint instead of refusing the mode: %v", where, err)
		}
	}
}

// TestRequireDiscoveredDoltBackendRefusesUnknownDoltMode pins the
// metadata-driven entry guard: a workspace whose metadata names a dolt_mode
// this build does not recognize must be refused with the shared mode
// refusal (the text command admission prints) before anything the guard
// fronts — metadata migration, server resolution, dialing, auto-start.
func TestRequireDiscoveredDoltBackendRefusesUnknownDoltMode(t *testing.T) {
	unknownDoltModeTestEndpoint(t)

	t.Run("metadata.json", func(t *testing.T) {
		beadsDir := t.TempDir()
		cfg := &configfile.Config{DoltMode: "future-mode", DoltServerHost: "127.0.0.1", DoltServerPort: 1}
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("save unknown-mode metadata: %v", err)
		}
		wantUnknownDoltModeRefusal(t, "RequireDiscoveredDoltBackend", beadsDir,
			RequireDiscoveredDoltBackend(beadsDir))
		// The refusal is read-only: the metadata it read is untouched.
		after, err := configfile.LoadForDiscovery(beadsDir)
		if err != nil || after == nil || after.DoltMode != "future-mode" {
			t.Fatalf("refusal changed the metadata it read: %v, %v", after, err)
		}
		for _, name := range []string{"dolt", "embeddeddolt", "proxieddb"} {
			if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
				t.Fatalf("refusal created %s (stat error: %v)", name, statErr)
			}
		}
	})

	t.Run("legacy config.json", func(t *testing.T) {
		beadsDir := t.TempDir()
		legacy := `{"backend":"dolt","dolt_mode":"future-mode","dolt_database":"beads_legacy","future_binding":"keep-me"}`
		legacyPath := filepath.Join(beadsDir, configfile.LegacyConfigFileName)
		if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
			t.Fatalf("write legacy config.json: %v", err)
		}
		err := RequireDiscoveredDoltBackend(beadsDir)
		wantUnknownDoltModeRefusal(t, "RequireDiscoveredDoltBackend", beadsDir, err)
		if !strings.Contains(err.Error(), "config.json") || strings.Contains(err.Error(), "metadata.json") {
			t.Fatalf("refusal = %v, want it to name config.json and not a metadata.json that does not exist", err)
		}
		if got, readErr := os.ReadFile(legacyPath); readErr != nil || string(got) != legacy {
			t.Fatalf("refusal rewrote config.json: %q, %v", got, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, configfile.ConfigFileName)); !os.IsNotExist(statErr) {
			t.Fatalf("refusal migrated config.json into metadata.json (stat error: %v)", statErr)
		}
	})
}

// TestConfigConstructorsRefuseUnknownDoltModeBeforeDialing pins that every
// metadata-driven constructor refuses an unknown dolt_mode before it can
// resolve an endpoint or dial one: at the test's pinned endpoint the failure
// without the refusal is a fast connection error, so the mode refusal is the
// ordering proof. A legacy config.json workspace is refused before Load can
// migrate it.
func TestConfigConstructorsRefuseUnknownDoltModeBeforeDialing(t *testing.T) {
	unknownDoltModeTestEndpoint(t)

	constructors := map[string]func(beadsDir string) (*DoltStore, error){
		"default": func(beadsDir string) (*DoltStore, error) {
			return NewFromConfig(t.Context(), beadsDir)
		},
		"standard": func(beadsDir string) (*DoltStore, error) {
			return NewFromConfigWithOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true})
		},
		"cli": func(beadsDir string) (*DoltStore, error) {
			return NewFromConfigWithCLIOptions(t.Context(), beadsDir, &Config{DisableAutoStart: true})
		},
	}

	t.Run("metadata.json", func(t *testing.T) {
		beadsDir := t.TempDir()
		cfg := &configfile.Config{DoltMode: "future-mode", DoltServerHost: "127.0.0.1", DoltServerPort: 1}
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("save unknown-mode metadata: %v", err)
		}
		for name, construct := range constructors {
			store, err := construct(beadsDir)
			if err == nil {
				_ = store.Close()
				t.Fatalf("%s constructor opened an unknown dolt_mode workspace", name)
			}
			wantUnknownDoltModeRefusal(t, name+" constructor", beadsDir, err)
		}
	})

	t.Run("legacy config.json", func(t *testing.T) {
		beadsDir := t.TempDir()
		legacy := `{"backend":"dolt","dolt_mode":"future-mode","dolt_database":"beads_legacy","future_binding":"keep-me"}`
		legacyPath := filepath.Join(beadsDir, configfile.LegacyConfigFileName)
		if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
			t.Fatalf("write legacy config.json: %v", err)
		}
		for name, construct := range constructors {
			store, err := construct(beadsDir)
			if err == nil {
				_ = store.Close()
				t.Fatalf("%s constructor migrated and opened a legacy config.json workspace with unknown dolt_mode", name)
			}
			wantUnknownDoltModeRefusal(t, name+" constructor", beadsDir, err)
			if got, readErr := os.ReadFile(legacyPath); readErr != nil || string(got) != legacy {
				t.Fatalf("%s constructor rewrote config.json: %q, %v", name, got, readErr)
			}
			if _, statErr := os.Stat(filepath.Join(beadsDir, configfile.ConfigFileName)); !os.IsNotExist(statErr) {
				t.Fatalf("%s constructor migrated config.json into metadata.json (stat error: %v)", name, statErr)
			}
		}
	})
}
