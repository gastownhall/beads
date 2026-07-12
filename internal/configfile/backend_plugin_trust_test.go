package configfile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveBackendPluginConfigLocalConfig(t *testing.T) {
	beadsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, BackendPluginLocalConfigFileName), []byte(`
backend_plugins:
  doltlite:
    command: /opt/bd-backend-doltlite
    args: ["--trace", "/tmp/plugin.jsonl", "serve"]
`), 0o600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	cfg, err := ResolveBackendPluginConfig(beadsDir, "doltlite")
	if err != nil {
		t.Fatalf("ResolveBackendPluginConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("ResolveBackendPluginConfig returned nil")
	}
	if cfg.Command != "/opt/bd-backend-doltlite" {
		t.Fatalf("Command = %q", cfg.Command)
	}
	wantArgs := []string{"--trace", "/tmp/plugin.jsonl", "serve"}
	if !reflect.DeepEqual(cfg.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", cfg.Args, wantArgs)
	}
}

func TestResolveBackendPluginConfigEnvWins(t *testing.T) {
	t.Setenv("BEADS_BACKEND_PLUGIN_COMMAND", "/env/plugin")
	t.Setenv("BEADS_BACKEND_PLUGIN_ARGS", `--trace "/tmp/plugin trace.jsonl" serve`)
	beadsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, BackendPluginLocalConfigFileName), []byte(`
backend_plugins:
  doltlite:
    command: /local/plugin
`), 0o600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	cfg, err := ResolveBackendPluginConfig(beadsDir, "doltlite")
	if err != nil {
		t.Fatalf("ResolveBackendPluginConfig: %v", err)
	}
	if cfg == nil || cfg.Command != "/env/plugin" {
		t.Fatalf("cfg = %#v, want env command", cfg)
	}
	wantArgs := []string{"--trace", "/tmp/plugin trace.jsonl", "serve"}
	if !reflect.DeepEqual(cfg.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", cfg.Args, wantArgs)
	}
}

func TestResolveBackendPluginConfigDoltIsBuiltin(t *testing.T) {
	cfg, err := ResolveBackendPluginConfig(t.TempDir(), BackendDolt)
	if err != nil {
		t.Fatalf("ResolveBackendPluginConfig: %v", err)
	}
	if cfg != nil {
		t.Fatalf("cfg = %#v, want nil for built-in dolt", cfg)
	}
}
