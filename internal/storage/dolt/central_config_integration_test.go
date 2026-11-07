package dolt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestApplyCentralConfigDefaults_WiresIntoOpenPath verifies that the
// applyCentralConfigDefaults glue function loads a central config file and
// takes only its dolt_mode into the per-project config. The connection
// fields stay empty: the getters and the port chain read the central config
// below config.yaml (GH#3895).
func TestApplyCentralConfigDefaults_WiresIntoOpenPath(t *testing.T) {
	// Create a temp central config file.
	tmpDir := t.TempDir()
	centralPath := filepath.Join(tmpDir, "server.json")
	central := &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "central.example.com",
		DoltServerPort: 3308,
		DoltServerUser: "centraluser",
		DoltServerTLS:  true,
	}
	data, err := json.MarshalIndent(central, "", "  ")
	if err != nil {
		t.Fatalf("marshal central config: %v", err)
	}
	if err := os.WriteFile(centralPath, data, 0o600); err != nil {
		t.Fatalf("write central config: %v", err)
	}

	// Point to our temp central config.
	t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)

	// Start with an empty project config (no server fields set).
	fileCfg := configfile.DefaultConfig()

	applyCentralConfigDefaults(fileCfg)

	if fileCfg.DoltMode != configfile.DoltModeServer {
		t.Errorf("DoltMode = %q, want %q", fileCfg.DoltMode, configfile.DoltModeServer)
	}
	if fileCfg.DoltServerHost != "" || fileCfg.DoltServerPort != 0 || fileCfg.DoltServerUser != "" || fileCfg.DoltServerTLS {
		t.Errorf("connection fields copied into the project config: host %q port %d user %q tls %v",
			fileCfg.DoltServerHost, fileCfg.DoltServerPort, fileCfg.DoltServerUser, fileCfg.DoltServerTLS)
	}
}

// TestApplyCentralConfigDefaults_MissingFileIsNoOp verifies that a
// missing central config file does not error or modify the project config.
func TestApplyCentralConfigDefaults_MissingFileIsNoOp(t *testing.T) {
	t.Setenv("BEADS_CENTRAL_CONFIG", "/nonexistent/path/server.json")

	fileCfg := configfile.DefaultConfig()
	fileCfg.DoltServerHost = "project.local"

	applyCentralConfigDefaults(fileCfg)

	if fileCfg.DoltServerHost != "project.local" {
		t.Errorf("DoltServerHost = %q, want project.local (should be unchanged)", fileCfg.DoltServerHost)
	}
}

// TestApplyCentralConfigDefaults_ProjectOverrides verifies that non-zero
// project fields are not overwritten by central defaults.
func TestApplyCentralConfigDefaults_ProjectOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	centralPath := filepath.Join(tmpDir, "server.json")
	central := &configfile.Config{
		DoltServerHost: "central.example.com",
		DoltServerPort: 3308,
	}
	data, err := json.MarshalIndent(central, "", "  ")
	if err != nil {
		t.Fatalf("marshal central config: %v", err)
	}
	if err := os.WriteFile(centralPath, data, 0o600); err != nil {
		t.Fatalf("write central config: %v", err)
	}

	t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)

	fileCfg := configfile.DefaultConfig()
	fileCfg.DoltServerHost = "project.local"
	fileCfg.DoltServerPort = 9999

	applyCentralConfigDefaults(fileCfg)

	if fileCfg.DoltServerHost != "project.local" {
		t.Errorf("DoltServerHost = %q, want project.local (project override)", fileCfg.DoltServerHost)
	}
	if fileCfg.DoltServerPort != 9999 {
		t.Errorf("DoltServerPort = %d, want 9999 (project override)", fileCfg.DoltServerPort)
	}
}

// TestLibraryOpenResolvesCentralConfigLikeTheCLI pins GH#3895 for the library
// open (NewFromConfig*: bd doctor's store checks, routed stores, the Go SDK):
// in a server-mode workspace whose metadata.json names no endpoint, it must
// resolve the server.json endpoint and user exactly as the CLI's store open
// (cmd/bd/main.go resolveDoltServerConnection) does, and on both paths a
// config.yaml dolt.host outranks the server.json host. The library open used
// to copy server.json's fields into metadata.json's, above config.yaml.
func TestLibraryOpenResolvesCentralConfigLikeTheCLI(t *testing.T) {
	const port = 15123
	for _, tc := range []struct {
		name     string
		yaml     string
		wantHost string
	}{
		{name: "server.json alone", wantHost: "db.invalid"},
		{name: "config.yaml dolt.host outranks it", yaml: "dolt.host: yaml.invalid\n", wantHost: "yaml.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := storeEndpointTestEnv(t, nil)
			central := filepath.Join(t.TempDir(), "server.json")
			body := fmt.Sprintf(`{"dolt_server_host":"db.invalid","dolt_server_port":%d,"dolt_server_user":"central_user"}`, port)
			if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEADS_CENTRAL_CONFIG", central)
			meta := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "central"}
			if err := meta.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			if tc.yaml != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(tc.yaml), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BEADS_DIR", beadsDir)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			// The library open, as NewFromConfigWithOptions resolves it.
			libFile, err := configfile.Load(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			applyCentralConfigDefaults(libFile)
			lib := &Config{}
			if err := applyResolvedConfig(context.Background(), beadsDir, libFile, lib); err != nil {
				t.Fatalf("applyResolvedConfig: %v", err)
			}
			applyConfigDefaults(lib)

			// The CLI's store open, as resolveDoltServerConnection resolves it.
			cliFile, err := configfile.Load(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			cli := &Config{BeadsDir: beadsDir, ServerHost: configfile.UnbracketHost(cliFile.GetDoltServerHost())}
			ApplyResolvedServerPort(beadsDir, cli)
			cli.ServerUser = cliFile.GetDoltServerUser()

			for _, got := range []struct {
				path string
				cfg  *Config
			}{{"library open", lib}, {"CLI open", cli}} {
				if got.cfg.ServerHost != tc.wantHost || got.cfg.ServerPort != port || got.cfg.ServerUser != "central_user" {
					t.Errorf("%s dials %s:%d as %q, want %s:%d as central_user",
						got.path, got.cfg.ServerHost, got.cfg.ServerPort, got.cfg.ServerUser, tc.wantHost, port)
				}
			}
		})
	}
}
