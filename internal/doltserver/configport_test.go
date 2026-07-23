package doltserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigReadsConfigYamlPort(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("listener:\n  host: 127.0.0.1\n  port: 51234\n")
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfig(beadsDir).Port; got != 51234 {
		t.Fatalf("DefaultConfig port = %d, want 51234 (from config.yaml listener.port)", got)
	}
}

func TestConfigYamlPortAbsentReturnsZero(t *testing.T) {
	if got := configYamlPort(t.TempDir()); got != 0 {
		t.Fatalf("configYamlPort on dir without config.yaml = %d, want 0", got)
	}
}
