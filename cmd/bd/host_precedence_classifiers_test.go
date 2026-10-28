package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestHostClassifiersFollowConfigYamlHostOverMetadataHost pins GH#5961 in the
// two cmd/bd classifiers that hand-roll the effective-host chain instead of
// calling configfile: clientServerShareFilesystem (auto-backup) and
// targetSelectsServerMode (foreign-workspace admission). A workspace whose
// metadata.json names the local 127.0.0.1 and whose config.yaml names the
// remote db.example.com is the reporter's leak; with config.yaml promoted
// (AC-1) and inference following the effective host (AC-3), the filesystem
// is remote — auto-backup's file:// URL would not be meaningful there — and
// bd run inside it selects a Dolt sql-server.
func TestHostClassifiersFollowConfigYamlHostOverMetadataHost(t *testing.T) {
	// Both classifiers read the process's BEADS_* environment.
	for _, name := range []string{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", t.TempDir())

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt.host: db.example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	meta := &configfile.Config{Backend: configfile.BackendDolt, DoltServerHost: "127.0.0.1"}
	if err := meta.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	t.Run("clientServerShareFilesystem", func(t *testing.T) {
		if clientServerShareFilesystem() {
			t.Error("clientServerShareFilesystem() = true, want false: the effective host is the remote config.yaml dolt.host over the metadata.json dolt_server_host")
		}
	})

	t.Run("targetSelectsServerMode", func(t *testing.T) {
		cfg, err := configfile.LoadForDiscovery(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if !targetSelectsServerMode(beadsDir, cfg) {
			t.Error("targetSelectsServerMode() = false, want true: bd run inside selects a Dolt server, the remote host its own config.yaml names over the metadata host")
		}
	})
}
