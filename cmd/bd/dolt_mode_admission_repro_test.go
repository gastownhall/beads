package main

import (
	"os"
	"path/filepath"
	"testing"
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
