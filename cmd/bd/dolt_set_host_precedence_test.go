package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestDoltSetHostWritesConfigYamlAndMetadata pins the writer half of GH#5961:
// `bd dolt set host` must write BOTH .beads/config.yaml dolt.host (which now
// outranks, AC-1) and .beads/metadata.json dolt_server_host when the workspace
// has a .beads/config.yaml — a metadata-only write would change nothing a
// dial reads — and only metadata.json when it does not, creating no
// config.yaml, warning only when a higher layer (BEADS_DOLT_SERVER_HOST,
// BD_DOLT_HOST, a user-level dolt.host) still outranks the value just set.
func TestDoltSetHostWritesConfigYamlAndMetadata(t *testing.T) {
	// A server-mode workspace whose metadata.json names 192.0.2.50, with a
	// .beads/config.yaml naming the same host when withConfigYaml is set.
	newWorkspace := func(t *testing.T, withConfigYaml bool) string {
		t.Helper()
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		// 0700: bd warns about a group/world-readable .beads, and the warning
		// would land in the captured output under test.
		if err := os.MkdirAll(beadsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := configfile.DefaultConfig()
		cfg.Backend = configfile.BackendDolt
		cfg.DoltMode = configfile.DoltModeServer
		cfg.DoltServerHost = "192.0.2.50"
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatal(err)
		}
		if withConfigYaml {
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
				[]byte("dolt.host: 192.0.2.50\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// A fresh HOME keeps the user-level config.yaml out of the recheck.
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BEADS_DIR", beadsDir)
		t.Setenv("BEADS_DOLT_SERVER_HOST", "")
		t.Setenv("BD_DOLT_HOST", "")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)
		oldCwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldCwd) })
		return beadsDir
	}

	// effectiveHost is the host a dial in this workspace resolves: the value
	// `bd dolt set host` claims to have set.
	effectiveHost := func(t *testing.T, beadsDir string) string {
		t.Helper()
		cfg, err := configfile.Load(beadsDir)
		if err != nil || cfg == nil {
			t.Fatalf("configfile.Load: %v (%v)", cfg, err)
		}
		var host string
		_ = config.WithBeadsDir(beadsDir, func() { host = cfg.GetDoltServerHost() })
		return host
	}

	t.Run("config.yaml exists: both files written", func(t *testing.T) {
		beadsDir := newWorkspace(t, true)
		origJsonOutput := jsonOutput
		defer func() { jsonOutput = origJsonOutput }()
		jsonOutput = true
		output := captureDoltSetOutput(t, "host", "192.0.2.60", false)

		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("output not pure JSON: %s", output)
		}
		if result["config_yaml_updated"] != true {
			t.Errorf("config_yaml_updated = %v, want true (the yaml write must run before the JSON report)", result["config_yaml_updated"])
		}
		loaded, err := configfile.Load(beadsDir)
		if err != nil || loaded == nil {
			t.Fatalf("configfile.Load: %v (%v)", loaded, err)
		}
		if loaded.DoltServerHost != "192.0.2.60" {
			t.Errorf("metadata.json dolt_server_host = %q, want 192.0.2.60", loaded.DoltServerHost)
		}
		if got := effectiveHost(t, beadsDir); got != "192.0.2.60" {
			t.Errorf("GetDoltServerHost() = %q, want 192.0.2.60: the dial must follow the host just set, so .beads/config.yaml dolt.host carries it too", got)
		}
	})

	t.Run("no config.yaml: metadata only, no file created", func(t *testing.T) {
		beadsDir := newWorkspace(t, false)
		output := captureDoltSetOutput(t, "host", "192.0.2.60", false)

		if _, err := os.Stat(filepath.Join(beadsDir, "config.yaml")); err == nil {
			t.Error("bd dolt set host created .beads/config.yaml; only metadata.json may be written when the workspace has none")
		}
		loaded, err := configfile.Load(beadsDir)
		if err != nil || loaded == nil {
			t.Fatalf("configfile.Load: %v (%v)", loaded, err)
		}
		if loaded.DoltServerHost != "192.0.2.60" {
			t.Errorf("metadata.json dolt_server_host = %q, want 192.0.2.60", loaded.DoltServerHost)
		}
		if got := effectiveHost(t, beadsDir); got != "192.0.2.60" {
			t.Errorf("GetDoltServerHost() = %q, want 192.0.2.60", got)
		}
		if strings.Contains(output, "still dials") {
			t.Errorf("nothing outranks the set host here, but the run warned:\n%s", output)
		}
	})

	t.Run("still-outranking source named", func(t *testing.T) {
		_ = newWorkspace(t, false)
		t.Setenv("BD_DOLT_HOST", "192.0.2.70")
		output := captureDoltSetOutput(t, "host", "192.0.2.60", false)
		if !strings.Contains(output, "still dials") ||
			!strings.Contains(output, "192.0.2.70") ||
			!strings.Contains(output, "BD_DOLT_HOST") {
			t.Errorf("warning must name BD_DOLT_HOST and the host it keeps bd at, got:\n%s", output)
		}
	})
}
