package configfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestDoltServerHostConfigYamlOutranksMetadata pins GH#5961: config.yaml
// dolt.host outranks the git-tracked metadata.json dolt_server_host, the same
// demotion the port chain gave dolt_server_port (GH#2372). A machine-local
// host committed in metadata.json must not travel to every clone above the
// receiving project's own config.yaml.
func TestDoltServerHostConfigYamlOutranksMetadata(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("dolt.host: 192.0.2.50\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	cfg := &Config{DoltServerHost: "127.0.0.1"}
	if got := cfg.GetDoltServerHost(); got != "192.0.2.50" {
		t.Errorf("GetDoltServerHost() = %q, want config.yaml dolt.host 192.0.2.50 over metadata.json dolt_server_host", got)
	}
}
