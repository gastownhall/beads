package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestValidateConfiguredBackendDoltMode pins admission's mode vocabulary:
// absent, empty and case-variant modes keep existing dispatch (the selectors
// compare with strings.ToLower), everything else is refused fail-closed with
// the shared mode refusal, and the existing backend-error precedence and
// registered-backend dispatch are untouched. A runtime server override never
// bypasses an explicitly unknown metadata mode.
func TestValidateConfiguredBackendDoltMode(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

	if err := validateConfiguredBackend(nil, beadsDir); err != nil {
		t.Fatalf("nil config rejected: %v", err)
	}
	for _, mode := range []string{
		"", configfile.DoltModeEmbedded, configfile.DoltModeServer, configfile.DoltModeProxiedServer,
		"Embedded", "SERVER", "Proxied-Server",
	} {
		cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: mode}
		if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
			t.Errorf("recognized dolt_mode %q refused: %v", mode, err)
		}
	}

	// Every other value is refused: typos, future modes, whitespace padding
	// (the selectors do not trim, so neither does admission), all-whitespace,
	// line breaks, and Unicode folds such as the long s in "ſerver" that
	// strings.EqualFold accepts but the mode selectors do not recognize.
	for _, mode := range []string{
		"future-mode", "proxiedserver", "embedded server",
		" embedded", "embedded ", "\t", "em\nbedded", "ſerver",
	} {
		cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: mode}
		err := validateConfiguredBackend(cfg, beadsDir)
		if err == nil {
			t.Errorf("unknown dolt_mode %q admitted", mode)
			continue
		}
		for _, want := range unknownDoltModeWantSubstrings(mode) {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("unknown dolt_mode %q refusal missing %q: %v", mode, want, err)
			}
		}
	}

	// Backend errors keep their precedence over the mode refusal: a removed
	// or unrecognized backend reports the backend, never the mode.
	for backend, marker := range map[string]string{
		configfile.BackendPostgres: "no longer supported",
		"mystery":                  "storage backend",
	} {
		err := validateConfiguredBackend(&configfile.Config{Backend: backend, DoltMode: "future-mode"}, beadsDir)
		if err == nil || !strings.Contains(err.Error(), marker) {
			t.Fatalf("backend %q with unknown mode: error = %v, want the backend refusal naming %q", backend, err, marker)
		}
		if strings.Contains(err.Error(), "dolt_mode") {
			t.Errorf("backend %q refusal reports the mode instead of the backend: %v", backend, err)
		}
	}

	// A registered non-Dolt backend dispatches to the registry regardless of
	// an arbitrary Dolt mode value: the mode is Dolt-only vocabulary.
	registerContractBackend(t, "unknown-mode-contract")
	if err := validateConfiguredBackend(&configfile.Config{Backend: "unknown-mode-contract", DoltMode: "nonsense"}, beadsDir); err != nil {
		t.Fatalf("registered backend refused over an arbitrary dolt_mode: %v", err)
	}

	// A runtime server override selects server mode, but it cannot rescue an
	// explicitly unknown metadata mode: the workspace would still be routed
	// by a value no selector recognizes.
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "dolt.example.invalid")
	err := validateConfiguredBackend(&configfile.Config{DoltMode: "future-mode"}, beadsDir)
	if err == nil || !strings.Contains(err.Error(), "future-mode") {
		t.Fatalf("server overrides bypassed an explicitly unknown dolt_mode: %v", err)
	}
}

// TestDoctorRejectsUnknownDoltModeBeforeDoltChecks pins that doctor reports
// the admission refusal without opening storage: doctor is the repair path a
// mixed-version operator reaches for, so it must name the mode, the file and
// the guarantee rather than run Dolt diagnostics against a workspace whose
// mode vocabulary it cannot resolve.
func TestDoctorRejectsUnknownDoltModeBeforeDoltChecks(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	cfg := &configfile.Config{
		DoltMode:       "future-mode",
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 1,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save unknown-mode metadata: %v", err)
	}

	cmd := exec.Command(bd, "doctor", "--server", "--json")
	cmd.Dir = root
	cmd.Env = append(removedBackendTestEnv(beadsDir), "BEADS_DOLT_AUTO_START=0", "BD_DISABLE_METRICS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("doctor unexpectedly ran Dolt checks for unknown dolt_mode:\n%s", out)
	}
	for _, want := range unknownDoltModeWantSubstrings("future-mode") {
		if !strings.Contains(string(out), want) {
			t.Errorf("doctor unknown-mode error missing %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db", "dolt-server.pid", "dolt-server.port"} {
		if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("doctor created %s for an unknown dolt_mode (stat error: %v)", name, statErr)
		}
	}
}
