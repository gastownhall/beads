package configfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsKnownDoltMode pins the mode vocabulary admission refuses on: the
// empty value (inference keeps existing behavior) and case variants the
// storage-mode selectors recognize (strings.ToLower comparisons), nothing
// else. Whitespace is not trimmed and Unicode folds such as the long s in
// "ſerver" are not accepted, because the selectors do not recognize them
// either — admitting a value the store would not select is exactly the
// silent embedded fall-through of GH#5104.
func TestIsKnownDoltMode(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{"", true},
		{DoltModeEmbedded, true},
		{DoltModeServer, true},
		{DoltModeProxiedServer, true},
		{"Embedded", true},
		{"SERVER", true},
		{"Proxied-Server", true},
		{"future-mode", false},
		{"proxiedserver", false},
		{"embedded server", false},
		{" embedded", false},
		{"embedded ", false},
		{"\t", false},
		{"em\nbedded", false},
		{"ſerver", false},
	}
	for _, tt := range tests {
		if got := IsKnownDoltMode(tt.mode); got != tt.want {
			t.Errorf("IsKnownDoltMode(%q) = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

// TestUnknownDoltModeErrorAt pins the shared refusal text: it names the
// offending value, the metadata file the mode was read from (a legacy
// config.json, not a metadata.json that does not exist), the supported
// vocabulary, the data-safety guarantee and the upgrade-or-correct remedy,
// and it never recommends deleting the field or substituting "embedded".
// Values and paths that do not print as themselves are spelled by the
// remedy formatting, so a line break inside a value cannot split the error.
func TestUnknownDoltModeErrorAt(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

	err := UnknownDoltModeErrorAt("future-mode", beadsDir)
	if err == nil {
		t.Fatal("UnknownDoltModeErrorAt returned nil")
	}
	for _, want := range []string{
		"dolt_mode", "future-mode", "metadata.json",
		DoltModeEmbedded, DoltModeServer, DoltModeProxiedServer,
		BackendNotOpenedGuarantee, "upgrade",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-mode error missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), `or delete the "dolt_mode"`) {
		t.Errorf("unknown-mode error recommends deleting the field: %v", err)
	}

	// A workspace with only a legacy config.json is told to fix that file.
	if writeErr := os.WriteFile(filepath.Join(beadsDir, LegacyConfigFileName), []byte("{}"), 0o600); writeErr != nil {
		t.Fatalf("write legacy config.json: %v", writeErr)
	}
	err = UnknownDoltModeErrorAt("future-mode", beadsDir)
	if err == nil {
		t.Fatal("UnknownDoltModeErrorAt returned nil for a legacy config.json workspace")
	}
	if !strings.Contains(err.Error(), "config.json") || strings.Contains(err.Error(), "metadata.json") {
		t.Errorf("unknown-mode error = %v, want it to name config.json and not a metadata.json that does not exist", err)
	}

	// A value carrying a line break is spelled, never printed: the refusal
	// stays one line and cannot be split by the value it reports.
	err = UnknownDoltModeErrorAt("bad\nmode", beadsDir)
	if err == nil {
		t.Fatal("UnknownDoltModeErrorAt returned nil for an injected value")
	}
	if strings.ContainsAny(err.Error(), "\n") {
		t.Errorf("unknown-mode error carries a raw line break: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "bad\\nmode") {
		t.Errorf("unknown-mode error does not spell the injected line break: %v", err)
	}
}

// TestLoadToleratesUnknownDoltMode pins that parsing stays permissive about
// the mode vocabulary: Load and LoadForDiscovery only report malformed
// metadata, and admission — not the loader — owns refusing an unrecognized
// dolt_mode. Doctor reads the metadata through the same loaders to report
// that refusal.
func TestLoadToleratesUnknownDoltMode(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"future-mode","dolt_database":"beads","future_binding":"keep-me"}`
	if err := os.WriteFile(ConfigPath(beadsDir), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	cfg, err := LoadForDiscovery(beadsDir)
	if err != nil || cfg == nil || cfg.DoltMode != "future-mode" {
		t.Fatalf("LoadForDiscovery = %v, %v; want the unknown mode parsed, not refused", cfg, err)
	}
	cfg, err = Load(beadsDir)
	if err != nil || cfg == nil || cfg.DoltMode != "future-mode" {
		t.Fatalf("Load = %v, %v; want the unknown mode parsed, not refused", cfg, err)
	}

	legacyDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	legacy := `{"backend":"dolt","dolt_mode":"future-mode","dolt_database":"beads_legacy","future_binding":"keep-me"}`
	legacyPath := filepath.Join(legacyDir, LegacyConfigFileName)
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config.json: %v", err)
	}
	cfg, err = LoadForDiscovery(legacyDir)
	if err != nil || cfg == nil || cfg.DoltMode != "future-mode" {
		t.Fatalf("LoadForDiscovery on legacy config.json = %v, %v; want the unknown mode parsed in place", cfg, err)
	}
	if got, readErr := os.ReadFile(legacyPath); readErr != nil || string(got) != legacy {
		t.Fatalf("LoadForDiscovery rewrote config.json: %q, %v", got, readErr)
	}
}
