package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestContextInfo_ServerModeIdentity verifies that loading a server-mode
// metadata.json produces a config whose identity fields (host, port, database,
// mode) match what was persisted. Drift scenario: GH#2438.
func TestContextInfo_ServerModeIdentity(t *testing.T) {
	// Unset env vars that override config port values
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       "server",
		DoltServerHost: "192.168.1.50",
		DoltServerPort: 3309,
		DoltDatabase:   "project_beads",
		ProjectID:      "proj-abc123",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded config is nil")
	}

	if got := loaded.GetDoltMode(); got != "server" {
		t.Errorf("DoltMode: got %q, want %q", got, "server")
	}
	if !loaded.IsDoltServerMode() {
		t.Error("IsDoltServerMode() returned false, want true")
	}
	if got := loaded.GetDoltServerHost(); got != "192.168.1.50" {
		t.Errorf("ServerHost: got %q, want %q", got, "192.168.1.50")
	}
	if got := loaded.GetDoltServerPort(); got != 3309 {
		t.Errorf("ServerPort: got %d, want %d", got, 3309)
	}
	if got := loaded.GetDoltDatabase(); got != "project_beads" {
		t.Errorf("Database: got %q, want %q", got, "project_beads")
	}
}

// TestContextInfo_EmbeddedModeIdentity verifies that the default (embedded)
// mode config reports embedded mode with no server host/port overrides.
func TestContextInfo_EmbeddedModeIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &configfile.Config{
		Database: "dolt",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded config is nil")
	}

	if got := loaded.GetDoltMode(); got != "embedded" {
		t.Errorf("DoltMode: got %q, want %q", got, "embedded")
	}
	if loaded.IsDoltServerMode() {
		t.Error("IsDoltServerMode() returned true for embedded config")
	}
	// Embedded mode should have empty host/port in the raw struct
	if loaded.DoltServerHost != "" {
		t.Errorf("DoltServerHost should be empty for embedded, got %q", loaded.DoltServerHost)
	}
	if loaded.DoltServerPort != 0 {
		t.Errorf("DoltServerPort should be 0 for embedded, got %d", loaded.DoltServerPort)
	}
}

// TestContextInfo_DataDirOverride verifies that a custom dolt_data_dir set in
// metadata.json is reflected when loaded. Uses a relative path since Save()
// strips absolute paths (GH#2251).
func TestContextInfo_DataDirOverride(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Save() strips absolute DoltDataDir, so write JSON directly to test
	// that the field round-trips for relative paths.
	cfg := &configfile.Config{
		Database:    "dolt",
		DoltDataDir: "custom-data",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded config is nil")
	}

	if got := loaded.GetDoltDataDir(); got != "custom-data" {
		t.Errorf("DoltDataDir: got %q, want %q", got, "custom-data")
	}

	// Also verify that an absolute path set via env var overrides the config
	t.Run("AbsoluteViaEnv", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_DATA_DIR", "/mnt/fast-ssd/dolt-data")
		if got := loaded.GetDoltDataDir(); got != "/mnt/fast-ssd/dolt-data" {
			t.Errorf("DoltDataDir with env override: got %q, want %q", got, "/mnt/fast-ssd/dolt-data")
		}
	})
}

// TestContextInfo_ProjectIDPresent verifies that project_id from metadata.json
// appears in the loaded config and would be surfaced in ContextInfo.
func TestContextInfo_ProjectIDPresent(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &configfile.Config{
		Database:  "dolt",
		ProjectID: "proj-deadbeef-1234",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded config is nil")
	}

	if loaded.ProjectID != "proj-deadbeef-1234" {
		t.Errorf("ProjectID: got %q, want %q", loaded.ProjectID, "proj-deadbeef-1234")
	}

	// Verify it would populate ContextInfo correctly
	info := ContextInfo{
		Backend:   configfile.BackendDolt,
		DoltMode:  loaded.GetDoltMode(),
		Database:  loaded.GetDoltDatabase(),
		ProjectID: loaded.ProjectID,
	}
	if info.ProjectID != "proj-deadbeef-1234" {
		t.Errorf("ContextInfo.ProjectID: got %q, want %q", info.ProjectID, "proj-deadbeef-1234")
	}
}

// TestContextInfo_EnvVarOverrides verifies that BEADS_DOLT_SERVER_HOST env var
// overrides the value from metadata.json. This is the drift vector from
// GH#2438: env vars and config can diverge silently.
func TestContextInfo_EnvVarOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       "server",
		DoltServerHost: "192.168.1.50",
		DoltServerPort: 3309,
		DoltDatabase:   "project_beads",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Before env override: host comes from config
	if got := loaded.GetDoltServerHost(); got != "192.168.1.50" {
		t.Errorf("before override: got %q, want %q", got, "192.168.1.50")
	}

	// Set env var override — simulates drift between config file and runtime
	t.Setenv("BEADS_DOLT_SERVER_HOST", "10.0.0.99")

	if got := loaded.GetDoltServerHost(); got != "10.0.0.99" {
		t.Errorf("after BEADS_DOLT_SERVER_HOST override: got %q, want %q", got, "10.0.0.99")
	}

	// The config struct still has the original value — this is the drift
	if loaded.DoltServerHost != "192.168.1.50" {
		t.Errorf("struct field should still be %q, got %q", "192.168.1.50", loaded.DoltServerHost)
	}

	// Verify port override works similarly
	t.Setenv("BEADS_DOLT_SERVER_PORT", "3310")
	if got := loaded.GetDoltServerPort(); got != 3310 {
		t.Errorf("after BEADS_DOLT_SERVER_PORT override: got %d, want %d", got, 3310)
	}

	// Verify database override works similarly
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "other_db")
	if got := loaded.GetDoltDatabase(); got != "other_db" {
		t.Errorf("after BEADS_DOLT_SERVER_DATABASE override: got %q, want %q", got, "other_db")
	}

	// Build a ContextInfo the same way context_cmd.go does and verify
	// it reflects the env-overridden values (the "effective" identity)
	info := ContextInfo{
		Backend:    configfile.BackendDolt,
		DoltMode:   loaded.GetDoltMode(),
		ServerHost: loaded.GetDoltServerHost(),
		ServerPort: loaded.GetDoltServerPort(),
		Database:   loaded.GetDoltDatabase(),
	}
	if info.ServerHost != "10.0.0.99" {
		t.Errorf("ContextInfo.ServerHost: got %q, want %q", info.ServerHost, "10.0.0.99")
	}
	if info.ServerPort != 3310 {
		t.Errorf("ContextInfo.ServerPort: got %d, want %d", info.ServerPort, 3310)
	}
	if info.Database != "other_db" {
		t.Errorf("ContextInfo.Database: got %q, want %q", info.Database, "other_db")
	}
}

// TestContextInfo_SaveStripAbsoluteDataDir verifies Save() strips absolute
// DoltDataDir paths (GH#2251 guard). This is relevant to drift because an
// absolute path in metadata.json would propagate to other clones and cause
// data loss.
func TestContextInfo_SaveStripAbsoluteDataDir(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &configfile.Config{
		Database:    "dolt",
		DoltDataDir: "/absolute/path/dolt-data",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Read raw JSON to verify absolute path was stripped
	data, err := os.ReadFile(filepath.Join(beadsDir, configfile.ConfigFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, exists := raw["dolt_data_dir"]; exists {
		var val string
		if err := json.Unmarshal(raw["dolt_data_dir"], &val); err == nil && val != "" {
			t.Errorf("absolute DoltDataDir should be stripped from saved JSON, got %q", val)
		}
	}
}
