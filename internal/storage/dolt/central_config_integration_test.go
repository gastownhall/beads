package dolt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestApplyCentralConfigDefaults_WiresIntoOpenPath verifies that the
// applyCentralConfigDefaults glue function loads a central config file
// and merges its server fields into the per-project config.
func TestApplyCentralConfigDefaults_WiresIntoOpenPath(t *testing.T) {
	// Create a temp central config file.
	tmpDir := t.TempDir()
	centralPath := filepath.Join(tmpDir, "server.json")
	central := &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "central.example.com",
		DoltServerPort: 3308,
		DoltServerUser: "centraluser",
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
	if fileCfg.DoltServerHost != "central.example.com" {
		t.Errorf("DoltServerHost = %q, want central.example.com", fileCfg.DoltServerHost)
	}
	if fileCfg.DoltServerPort != 3308 {
		t.Errorf("DoltServerPort = %d, want 3308", fileCfg.DoltServerPort)
	}
	if fileCfg.DoltServerUser != "centraluser" {
		t.Errorf("DoltServerUser = %q, want centraluser", fileCfg.DoltServerUser)
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
