package configfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// writeCentralConfig writes a Config as JSON to the given path.
func writeCentralConfig(t *testing.T, path string, cfg *Config) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("failed to create central config dir: %v", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal central config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("failed to write central config: %v", err)
	}
}

func TestLoadCentralConfig_FileExists(t *testing.T) {
	tmpHome := t.TempDir()
	centralPath := filepath.Join(tmpHome, ".config", "beads", "server.json")

	writeCentralConfig(t, centralPath, &Config{
		DoltMode:       DoltModeServer,
		DoltServerHost: "central.example.com",
		DoltServerPort: 3308,
		DoltServerUser: "centraluser",
		DoltServerTLS:  true,
	})

	cfg, err := LoadCentralConfig(centralPath)
	if err != nil {
		t.Fatalf("LoadCentralConfig() error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadCentralConfig() returned nil, want non-nil config")
	}
	if cfg.DoltServerHost != "central.example.com" {
		t.Errorf("DoltServerHost = %q, want central.example.com", cfg.DoltServerHost)
	}
	if cfg.DoltServerPort != 3308 {
		t.Errorf("DoltServerPort = %d, want 3308", cfg.DoltServerPort)
	}
	if cfg.DoltServerUser != "centraluser" {
		t.Errorf("DoltServerUser = %q, want centraluser", cfg.DoltServerUser)
	}
	if cfg.DoltMode != DoltModeServer {
		t.Errorf("DoltMode = %q, want %q", cfg.DoltMode, DoltModeServer)
	}
	if !cfg.DoltServerTLS {
		t.Error("DoltServerTLS = false, want true")
	}
}

func TestLoadCentralConfig_FileNotExists(t *testing.T) {
	cfg, err := LoadCentralConfig("/nonexistent/path/server.json")
	if err != nil {
		t.Fatalf("LoadCentralConfig() error: %v, want nil (graceful skip)", err)
	}
	if cfg != nil {
		t.Errorf("LoadCentralConfig() = %v, want nil for missing file", cfg)
	}
}

func TestLoadCentralConfig_InvalidJSON(t *testing.T) {
	tmpHome := t.TempDir()
	centralPath := filepath.Join(tmpHome, ".config", "beads", "server.json")
	if err := os.MkdirAll(filepath.Dir(centralPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(centralPath, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCentralConfig(centralPath)
	if err == nil {
		t.Error("LoadCentralConfig() with invalid JSON should return error")
	}
}

func TestApplyCentralDefaults_FillsEmptyFields(t *testing.T) {
	central := &Config{
		DoltMode:       DoltModeServer,
		DoltServerHost: "central.example.com",
		DoltServerPort: 3308,
		DoltServerUser: "centraluser",
		DoltServerTLS:  true,
	}
	project := &Config{
		Database: "mydb",
	}

	ApplyCentralDefaults(project, central)

	if project.DoltMode != DoltModeServer {
		t.Errorf("DoltMode = %q, want %q", project.DoltMode, DoltModeServer)
	}
	if project.DoltServerHost != "central.example.com" {
		t.Errorf("DoltServerHost = %q, want central.example.com", project.DoltServerHost)
	}
	if project.DoltServerPort != 3308 {
		t.Errorf("DoltServerPort = %d, want 3308", project.DoltServerPort)
	}
	if project.DoltServerUser != "centraluser" {
		t.Errorf("DoltServerUser = %q, want centraluser", project.DoltServerUser)
	}
	if !project.DoltServerTLS {
		t.Error("DoltServerTLS = false, want true")
	}
}

func TestApplyCentralDefaults_ProjectOverridesCentral(t *testing.T) {
	central := &Config{
		DoltMode:       DoltModeServer,
		DoltServerHost: "central.example.com",
		DoltServerPort: 3308,
		DoltServerUser: "centraluser",
		DoltServerTLS:  true,
	}
	project := &Config{
		DoltMode:       DoltModeEmbedded,
		DoltServerHost: "project.local",
		DoltServerPort: 3309,
		DoltServerUser: "projuser",
		DoltServerTLS:  false, // zero value — cannot override to false
	}

	ApplyCentralDefaults(project, central)

	if project.DoltMode != DoltModeEmbedded {
		t.Errorf("DoltMode = %q, want %q (project override)", project.DoltMode, DoltModeEmbedded)
	}
	if project.DoltServerHost != "project.local" {
		t.Errorf("DoltServerHost = %q, want project.local (project override)", project.DoltServerHost)
	}
	if project.DoltServerPort != 3309 {
		t.Errorf("DoltServerPort = %d, want 3309 (project override)", project.DoltServerPort)
	}
	if project.DoltServerUser != "projuser" {
		t.Errorf("DoltServerUser = %q, want projuser (project override)", project.DoltServerUser)
	}
}

func TestApplyCentralDefaults_EnvVarsOverrideBoth(t *testing.T) {
	// Central config provides defaults
	central := &Config{
		DoltServerHost: "central.example.com",
		DoltServerPort: 3308,
	}
	project := &Config{}

	// Apply central defaults first
	ApplyCentralDefaults(project, central)

	// Now set env vars — the getter methods should pick these up
	t.Setenv("BEADS_DOLT_SERVER_HOST", "env.override.com")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")

	// Env vars should override the central config values via getter methods
	if got := project.GetDoltServerHost(); got != "env.override.com" {
		t.Errorf("GetDoltServerHost() = %q, want env.override.com", got)
	}
	if got := project.GetDoltServerPort(); got != 9999 {
		t.Errorf("GetDoltServerPort() = %d, want 9999", got)
	}
}

func TestApplyCentralDefaults_NonServerFieldsNotInherited(t *testing.T) {
	central := &Config{
		Database:     "central-db-name",
		DoltDatabase: "central-dolt-db",
		DoltDataDir:  "/central/data/dir",
		ProjectID:    "central-project-id",
	}
	project := &Config{}

	ApplyCentralDefaults(project, central)

	// Non-server fields must NOT be inherited from central config
	if project.Database != "" {
		t.Errorf("Database = %q, want empty (not inherited)", project.Database)
	}
	if project.DoltDatabase != "" {
		t.Errorf("DoltDatabase = %q, want empty (not inherited)", project.DoltDatabase)
	}
	if project.DoltDataDir != "" {
		t.Errorf("DoltDataDir = %q, want empty (not inherited)", project.DoltDataDir)
	}
	if project.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty (not inherited)", project.ProjectID)
	}
}

func TestApplyCentralDefaults_NilCentralIsNoOp(t *testing.T) {
	project := &Config{
		DoltServerHost: "project.local",
	}

	ApplyCentralDefaults(project, nil)

	if project.DoltServerHost != "project.local" {
		t.Errorf("DoltServerHost = %q, want project.local (nil central should be no-op)", project.DoltServerHost)
	}
}

func TestDefaultCentralConfigPath(t *testing.T) {
	path := DefaultCentralConfigPath()
	if path == "" {
		t.Fatal("DefaultCentralConfigPath() returned empty string")
	}
	// Should end with the expected path components
	if filepath.Base(path) != "server.json" {
		t.Errorf("DefaultCentralConfigPath() = %q, want filename server.json", path)
	}
	dir := filepath.Base(filepath.Dir(path))
	if dir != "beads" {
		t.Errorf("DefaultCentralConfigPath() parent dir = %q, want beads", dir)
	}
}

// TestCentralConfigIsLowestConnectionSource pins GH#3895's precedence: the
// central server config fills the host, user and TLS getters only where the
// environment, metadata.json and (for the host) config.yaml dolt.host name
// nothing, and it never selects server storage mode by itself.
func TestCentralConfigIsLowestConnectionSource(t *testing.T) {
	setup := func(t *testing.T) {
		t.Helper()
		for _, name := range []string{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_USER", "BEADS_DOLT_SERVER_TLS",
			"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER"} {
			t.Setenv(name, "")
		}
		centralPath := filepath.Join(t.TempDir(), "server.json")
		writeCentralConfig(t, centralPath, &Config{
			DoltServerHost: "central.example.com",
			DoltServerUser: "central_user",
			DoltServerTLS:  true,
		})
		t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)
		configDir := t.TempDir()
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		t.Cleanup(config.ResetForTesting)
	}

	t.Run("fills fields nothing else names", func(t *testing.T) {
		setup(t)
		cfg := &Config{Backend: BackendDolt}
		if got := cfg.GetDoltServerHost(); got != "central.example.com" {
			t.Errorf("GetDoltServerHost() = %q, want central.example.com", got)
		}
		if got := cfg.GetDoltServerUser(); got != "central_user" {
			t.Errorf("GetDoltServerUser() = %q, want central_user", got)
		}
		if !cfg.GetDoltServerTLS() {
			t.Error("GetDoltServerTLS() = false, want true from the central config")
		}
		if cfg.HostImpliesServerMode() || cfg.IsDoltServerMode() {
			t.Error("a central host selected server storage mode; only the workspace's own settings may")
		}
	})

	t.Run("env wins", func(t *testing.T) {
		setup(t)
		t.Setenv("BEADS_DOLT_SERVER_HOST", "10.0.0.1")
		t.Setenv("BEADS_DOLT_SERVER_USER", "env_user")
		t.Setenv("BEADS_DOLT_SERVER_TLS", "0")
		cfg := &Config{}
		if got := cfg.GetDoltServerHost(); got != "10.0.0.1" {
			t.Errorf("GetDoltServerHost() = %q, want 10.0.0.1", got)
		}
		if got := cfg.GetDoltServerUser(); got != "env_user" {
			t.Errorf("GetDoltServerUser() = %q, want env_user", got)
		}
		if cfg.GetDoltServerTLS() {
			t.Error("GetDoltServerTLS() = true, want BEADS_DOLT_SERVER_TLS=0 to disable it")
		}
	})

	t.Run("metadata.json wins", func(t *testing.T) {
		setup(t)
		cfg := &Config{DoltServerHost: "192.168.1.100", DoltServerUser: "meta_user"}
		if got := cfg.GetDoltServerHost(); got != "192.168.1.100" {
			t.Errorf("GetDoltServerHost() = %q, want 192.168.1.100", got)
		}
		if got := cfg.GetDoltServerUser(); got != "meta_user" {
			t.Errorf("GetDoltServerUser() = %q, want meta_user", got)
		}
	})

	t.Run("config.yaml dolt.host wins", func(t *testing.T) {
		setup(t)
		configDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("dolt.host: 100.64.0.1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", configDir)
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		cfg := &Config{}
		if got := cfg.GetDoltServerHost(); got != "100.64.0.1" {
			t.Errorf("GetDoltServerHost() = %q, want 100.64.0.1", got)
		}
		if got := cfg.GetDoltServerUser(); got != "central_user" {
			t.Errorf("GetDoltServerUser() = %q, want central_user (config.yaml names no user)", got)
		}
	})

	t.Run("no central config keeps the defaults", func(t *testing.T) {
		setup(t)
		t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "none.json"))
		cfg := &Config{}
		if got := cfg.GetDoltServerHost(); got != DefaultDoltServerHost {
			t.Errorf("GetDoltServerHost() = %q, want %q", got, DefaultDoltServerHost)
		}
		if got := cfg.GetDoltServerUser(); got != DefaultDoltServerUser {
			t.Errorf("GetDoltServerUser() = %q, want %q", got, DefaultDoltServerUser)
		}
		if cfg.GetDoltServerTLS() {
			t.Error("GetDoltServerTLS() = true with no TLS setting anywhere")
		}
	})
}
