package configfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// GH#5961: config.yaml dolt.host must outrank the git-tracked
// metadata.json dolt_server_host, matching the port demotion of #2372.
func TestDoltServerHostConfigYamlOutranksMetadata(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("dolt.host: 100.64.0.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	cfg := &Config{DoltServerHost: "127.0.0.1"}
	if got := cfg.GetDoltServerHost(); got != "100.64.0.1" {
		t.Errorf("GetDoltServerHost() = %q, want config.yaml dolt.host 100.64.0.1 over metadata.json dolt_server_host", got)
	}
}
