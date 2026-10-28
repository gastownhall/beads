package dolt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// TestApplyResolvedConfigCentralConfigParity pins GH#3895's library-path
// contract: the library open (both NewFromConfig* constructors route through
// applyResolvedConfig) resolves the same endpoint the CLI's hand-built open
// fills in (cmd/bd's resolveDoltServerConnection) — the central server
// config's host, port, user and TLS — because those fields now come from the
// configfile getters' central tier, not from a merge into the loaded
// metadata struct. That merge inverts precedence, and the open path must not
// do it: a config.yaml dolt.host outranks the central config's host on the
// library path too, exactly as it does on the CLI's. The merge the open path
// keeps is dolt_mode inheritance alone.
func TestApplyResolvedConfigCentralConfigParity(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_USER", "")
	t.Setenv("BEADS_DOLT_SERVER_TLS", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)

	centralPath := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(centralPath,
		[]byte(`{"dolt_server_host":"db.invalid","dolt_server_port":6007,"dolt_server_user":"central_user","dolt_server_tls":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)

	beadsDir := t.TempDir()
	if err := (&configfile.Config{
		Backend:  configfile.BackendDolt,
		DoltMode: configfile.DoltModeServer,
	}).Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	fileCfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	// The open path merge inherits dolt_mode only (AC-6): no connection
	// field lands in the struct, or it would outrank config.yaml for the
	// getters that follow.
	applyCentralConfigDefaults(fileCfg)
	if fileCfg.DoltServerHost != "" || fileCfg.DoltServerPort != 0 || fileCfg.DoltServerUser != "" || fileCfg.DoltServerTLS {
		t.Fatalf("the open-path merge copied connection fields into the loaded config: %+v", *fileCfg)
	}

	// The library resolves the central endpoint, with its source, like the
	// CLI's open does.
	cfg := &Config{}
	if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ServerHost != "db.invalid" {
		t.Errorf("ServerHost = %q, want db.invalid", cfg.ServerHost)
	}
	if cfg.ServerPort != 6007 || cfg.ServerPortSource != doltserver.PortSourceCentralConfig {
		t.Errorf("ServerPort = %d (%s), want 6007 (%s)", cfg.ServerPort, cfg.ServerPortSource, doltserver.PortSourceCentralConfig)
	}
	if cfg.ServerUser != "central_user" {
		t.Errorf("ServerUser = %q, want central_user", cfg.ServerUser)
	}
	if !cfg.ServerTLS {
		t.Error("ServerTLS = false, want true from the central server config")
	}

	// Same endpoint the CLI's own resolvers dial (resolveDoltServerConnection
	// reads the same getters and ResolveEndpoint).
	ep := doltserver.ResolveEndpoint(beadsDir, nil)
	if ep.Host != "db.invalid" || ep.Port != 6007 || ep.User != "central_user" || !ep.TLS {
		t.Errorf("ResolveEndpoint = %s@%s:%d tls=%v, want central_user@db.invalid:6007 tls=true", ep.User, ep.Host, ep.Port, ep.TLS)
	}

	// A config.yaml dolt.host outranks the central config's host on BOTH
	// paths.
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("dolt:\n  host: 10.9.8.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	fileCfg, err = configfile.Load(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	applyCentralConfigDefaults(fileCfg)
	cfg = &Config{}
	if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ServerHost != "10.9.8.7" {
		t.Errorf("ServerHost = %q, want 10.9.8.7: config.yaml dolt.host must outrank the central config's host on the library path too", cfg.ServerHost)
	}
}
