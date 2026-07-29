package configfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
