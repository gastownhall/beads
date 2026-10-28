package configfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// centralConfigFixture points BEADS_CENTRAL_CONFIG at a server.json naming
// a remote host, a central user and TLS on, in an isolated home, and returns
// the project config the getters run on.
func centralConfigFixture(t *testing.T) *Config {
	t.Helper()
	centralPath := filepath.Join(t.TempDir(), "server.json")
	writeCentralConfig(t, centralPath, &Config{
		DoltServerHost: "db.central.invalid",
		DoltServerUser: "central_user",
		DoltServerTLS:  true,
	})
	t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)
	return &Config{Backend: BackendDolt}
}

// TestServerFieldGettersCentralConfigPrecedence pins the central server
// config's rank for the four connection fields its getters read (GH#3895):
// below the environment, below metadata.json, and below config.yaml's
// dolt.host — filling in only the fields nothing above it names. The TLS env
// var is tri-state-shaped: any value that is not 1/true disables, so
// BEADS_DOLT_SERVER_TLS=0 still turns TLS off with the central config on.
func TestServerFieldGettersCentralConfigPrecedence(t *testing.T) {
	t.Run("central fills fields nothing else names", func(t *testing.T) {
		cfg := centralConfigFixture(t)
		if got := cfg.GetDoltServerHost(); got != "db.central.invalid" {
			t.Errorf("GetDoltServerHost() = %q, want db.central.invalid", got)
		}
		if got := cfg.GetDoltServerUser(); got != "central_user" {
			t.Errorf("GetDoltServerUser() = %q, want central_user", got)
		}
		if !cfg.GetDoltServerTLS() {
			t.Error("GetDoltServerTLS() = false, want true from the central config")
		}
	})

	t.Run("env wins and BEADS_DOLT_SERVER_TLS=0 disables", func(t *testing.T) {
		cfg := centralConfigFixture(t)
		t.Setenv("BEADS_DOLT_SERVER_HOST", "env.host.invalid")
		t.Setenv("BEADS_DOLT_SERVER_USER", "env_user")
		t.Setenv("BEADS_DOLT_SERVER_TLS", "0")
		if got := cfg.GetDoltServerHost(); got != "env.host.invalid" {
			t.Errorf("GetDoltServerHost() = %q, want env.host.invalid", got)
		}
		if got := cfg.GetDoltServerUser(); got != "env_user" {
			t.Errorf("GetDoltServerUser() = %q, want env_user", got)
		}
		if cfg.GetDoltServerTLS() {
			t.Error("GetDoltServerTLS() = true, want false: BEADS_DOLT_SERVER_TLS=0 disables the central config's TLS")
		}
	})

	t.Run("metadata.json wins", func(t *testing.T) {
		cfg := centralConfigFixture(t)
		cfg.DoltServerHost = "meta.host.invalid"
		cfg.DoltServerUser = "meta_user"
		cfg.DoltServerTLS = true
		if got := cfg.GetDoltServerHost(); got != "meta.host.invalid" {
			t.Errorf("GetDoltServerHost() = %q, want meta.host.invalid", got)
		}
		if got := cfg.GetDoltServerUser(); got != "meta_user" {
			t.Errorf("GetDoltServerUser() = %q, want meta_user", got)
		}
	})

	t.Run("config.yaml dolt.host wins over the central host", func(t *testing.T) {
		cfg := centralConfigFixture(t)
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
		if got := cfg.GetDoltServerHost(); got != "10.9.8.7" {
			t.Errorf("GetDoltServerHost() = %q, want 10.9.8.7 from config.yaml, not the central config's host", got)
		}
		// Only the host has a config.yaml tier; the user still comes from
		// the central config.
		if got := cfg.GetDoltServerUser(); got != "central_user" {
			t.Errorf("GetDoltServerUser() = %q, want central_user", got)
		}
	})
}
