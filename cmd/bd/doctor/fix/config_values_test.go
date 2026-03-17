package fix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

func TestConfigValues_RegeneratesMalformedRedirectSourceMetadata(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")

	repoDir := t.TempDir()
	targetRoot := t.TempDir()
	sourceBeadsDir := filepath.Join(repoDir, ".beads")
	targetBeadsDir := filepath.Join(targetRoot, ".beads")
	if err := os.MkdirAll(sourceBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir source beads dir: %v", err)
	}
	if err := os.MkdirAll(targetBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir target beads dir: %v", err)
	}

	targetCfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3318,
		DoltServerUser: "redirect-user",
		DoltDatabase:   "target_db",
	}
	if err := targetCfg.Save(targetBeadsDir); err != nil {
		t.Fatalf("save target config: %v", err)
	}
	targetBefore, err := os.ReadFile(filepath.Join(targetBeadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read target metadata before: %v", err)
	}

	malformed := `{"database":"beads.db","dolt_mode":"server","dolt_database":"source_db",`
	if err := os.WriteFile(filepath.Join(sourceBeadsDir, "metadata.json"), []byte(malformed), 0o600); err != nil {
		t.Fatalf("write malformed source metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceBeadsDir, beads.RedirectFileName), []byte(targetBeadsDir+"\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	if err := ConfigValues(repoDir); err != nil {
		t.Fatalf("ConfigValues failed: %v", err)
	}

	sourceCfg, err := configfile.Load(sourceBeadsDir)
	if err != nil {
		t.Fatalf("load regenerated source config: %v", err)
	}
	if sourceCfg == nil {
		t.Fatal("expected regenerated source config")
	}
	if sourceCfg.Database != "dolt" {
		t.Fatalf("source Database = %q, want %q", sourceCfg.Database, "dolt")
	}
	if sourceCfg.DoltDatabase != "source_db" {
		t.Fatalf("source DoltDatabase = %q, want %q", sourceCfg.DoltDatabase, "source_db")
	}
	if sourceCfg.DoltServerHost != "127.0.0.1" {
		t.Fatalf("source DoltServerHost = %q, want %q", sourceCfg.DoltServerHost, "127.0.0.1")
	}
	if sourceCfg.DoltServerPort != 3318 {
		t.Fatalf("source DoltServerPort = %d, want %d", sourceCfg.DoltServerPort, 3318)
	}

	targetAfter, err := os.ReadFile(filepath.Join(targetBeadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read target metadata after: %v", err)
	}
	if string(targetAfter) != string(targetBefore) {
		t.Fatal("target metadata.json was modified during redirected source repair")
	}
}
