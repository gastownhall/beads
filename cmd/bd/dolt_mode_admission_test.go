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

// TestValidateConfiguredBackendDoltMode pins storage admission's dolt_mode
// check: no mode and the recognized modes in any ASCII case keep their
// dispatch; any other value is refused with the shared refusal, even when an
// environment override would select a server; and the backend refusals keep
// their precedence, while a registered backend ignores dolt_mode entirely.
func TestValidateConfiguredBackendDoltMode(t *testing.T) {
	for _, key := range []string{"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		t.Setenv(key, "")
	}
	beadsDir := t.TempDir()

	if err := validateConfiguredBackend(nil, beadsDir); err != nil {
		t.Fatalf("validateConfiguredBackend(nil) = %v, want nil", err)
	}
	for mode, wantEmbedded := range map[string]bool{
		"":               true,
		"embedded":       true,
		"EMBEDDED":       true,
		"server":         false,
		"Server":         false,
		"proxied-server": false,
		"PROXIED-SERVER": false,
	} {
		for _, backend := range []string{"", configfile.BackendDolt} {
			cfg := &configfile.Config{Backend: backend, DoltMode: mode}
			if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
				t.Errorf("validateConfiguredBackend(backend=%q, dolt_mode=%q) = %v, want nil", backend, mode, err)
			}
			if got := selectsEmbeddedStore(beadsDir, cfg); got != wantEmbedded {
				t.Errorf("selectsEmbeddedStore(backend=%q, dolt_mode=%q) = %v, want %v", backend, mode, got, wantEmbedded)
			}
		}
	}

	unknown := []string{"some-future-mode", "proxied", " server", "server ", "   ", "\u017ferver", "server\nembedded"}
	for _, mode := range unknown {
		cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: mode}
		want := configfile.UnknownDoltModeErrorAt(mode, beadsDir).Error()
		if err := validateConfiguredBackend(cfg, beadsDir); err == nil || err.Error() != want {
			t.Errorf("validateConfiguredBackend(dolt_mode=%q) = %v, want %q", mode, err, want)
		}
	}

	// An environment override that selects a server does not admit an
	// explicitly unknown mode: the metadata says this build cannot read it.
	for _, override := range [][2]string{
		{"BEADS_DOLT_SERVER_MODE", "1"},
		{"BEADS_DOLT_SHARED_SERVER", "1"},
		{"BEADS_DOLT_SERVER_HOST", "10.0.0.5"},
	} {
		t.Run("override/"+override[0], func(t *testing.T) {
			t.Setenv(override[0], override[1])
			cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: "some-future-mode"}
			if err := validateConfiguredBackend(cfg, beadsDir); err == nil || !strings.Contains(err.Error(), `dolt_mode "some-future-mode"`) {
				t.Fatalf("validateConfiguredBackend with %s=%s = %v, want the unknown-dolt_mode refusal", override[0], override[1], err)
			}
		})
	}

	// Backend refusals come first, with their own text.
	if err := validateConfiguredBackend(&configfile.Config{Backend: "mystery", DoltMode: "bogus"}, beadsDir); err == nil ||
		err.Error() != configfile.UnknownBackendErrorAt("mystery", beadsDir, &configfile.Config{Backend: "mystery", DoltMode: "bogus"}).Error() {
		t.Errorf("unknown backend + unknown mode = %v, want the unknown-backend refusal", err)
	}
	removed := &configfile.Config{Backend: configfile.BackendPostgres, DoltMode: "bogus"}
	if err := validateConfiguredBackend(removed, beadsDir); err == nil || err.Error() != configfile.RemovedBackendErrorAt(configfile.BackendPostgres, beadsDir, removed).Error() {
		t.Errorf("removed backend + unknown mode = %v, want the removed-backend refusal", err)
	}

	// A registered backend does not read dolt_mode; it is dispatched as before.
	const registered = "dolt-mode-admission-contract"
	registerContractBackend(t, registered)
	cfg := &configfile.Config{Backend: registered, DoltMode: "anything-at-all"}
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		t.Errorf("registered backend with an arbitrary dolt_mode = %v, want nil", err)
	}
	if selectsEmbeddedStore(beadsDir, cfg) {
		t.Errorf("registered backend with an arbitrary dolt_mode selects the embedded store")
	}
}

// TestDoctorRejectsUnknownDoltModeBeforeDoltChecks pins that doctor, which
// parses the metadata to report on it, reports the unknown-dolt_mode refusal
// through its target admission guard without opening or creating storage.
func TestDoctorRejectsUnknownDoltModeBeforeDoltChecks(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := (&configfile.Config{Backend: configfile.BackendDolt, DoltMode: "some-future-mode", DoltDatabase: "beads"}).Save(beadsDir); err != nil {
		t.Fatalf("save unknown-mode metadata: %v", err)
	}

	cmd := exec.Command(bd, "doctor", "--json")
	cmd.Dir = root
	cmd.Env = append(removedBackendTestEnv(beadsDir), "BEADS_DOLT_AUTO_START=0", "BD_DISABLE_METRICS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("doctor ran on an unknown dolt_mode:\n%s", out)
	}
	for _, want := range []string{`dolt_mode \"some-future-mode\"`, configfile.BackendNotOpenedGuarantee} {
		if !strings.Contains(string(out), want) && !strings.Contains(string(out), strings.ReplaceAll(want, `\"`, `"`)) {
			t.Errorf("doctor unknown-dolt_mode error missing %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"embeddeddolt", "dolt", "proxieddb", "beads.db", ".local_version", "dolt-server.pid", "dolt-server.port"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("doctor created %s for an unknown dolt_mode (stat error: %v)", name, statErr)
		}
	}
}
