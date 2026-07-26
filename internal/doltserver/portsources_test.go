package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// Asserts the precedence chain against DefaultConfig itself by layering each
// source on top of the previous ones: env > port file > Dolt server
// config.yaml > Beads config.yaml > metadata.json (GH#4511).
func TestDefaultConfigPrecedenceChain(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	beadsDir := t.TempDir()

	// 1. Nothing configured: ephemeral (port 0).
	if got := DefaultConfig(beadsDir).Port; got != 0 {
		t.Fatalf("no source configured: port = %d, want 0", got)
	}

	// 2. metadata.json dolt_server_port is the lowest-priority source.
	writeMetadataPort(t, beadsDir, 5001)
	if got := DefaultConfig(beadsDir).Port; got != 5001 {
		t.Fatalf("metadata.json only: port = %d, want 5001", got)
	}

	// 3. Beads config.yaml (dolt.port) must beat metadata.json.
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("dolt.port: 5002\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
	if got := DefaultConfig(beadsDir).Port; got != 5002 {
		t.Fatalf("beads config.yaml + metadata.json: port = %d, want 5002 (yaml over metadata)", got)
	}

	// 4. Dolt server's own config.yaml (listener.port, in the dolt data dir)
	// must beat Beads config.yaml — these are two distinct sources with
	// distinct precedence, not one collapsed entry.
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.1\n  port: 5003\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfig(beadsDir).Port; got != 5003 {
		t.Fatalf("+ dolt server config.yaml: port = %d, want 5003 (dolt yaml over beads yaml)", got)
	}

	// 5. The port file must beat both YAML sources.
	if err := writePortFile(beadsDir, 5004); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfig(beadsDir).Port; got != 5004 {
		t.Fatalf("+ port file: port = %d, want 5004 (port file over both yaml sources)", got)
	}

	// 6. The env var override must beat everything, including the port file.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "5005")
	if got := DefaultConfig(beadsDir).Port; got != 5005 {
		t.Fatalf("+ env var: port = %d, want 5005 (env over port file)", got)
	}
}

func writeMetadataPort(t *testing.T, beadsDir string, port int) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"dolt_server_port": port})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
