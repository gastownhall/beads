package fix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

func TestResolveRuntimeInfoForRepo_PreservesRedirectSourceDatabase(t *testing.T) {
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

	sourceCfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "source_db",
	}
	if err := sourceCfg.Save(sourceBeadsDir); err != nil {
		t.Fatalf("save source config: %v", err)
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

	if err := os.WriteFile(filepath.Join(sourceBeadsDir, beads.RedirectFileName), []byte(targetBeadsDir+"\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)

	info, err := resolveFixRuntimeInfoForRepo(repoDir)
	if err != nil {
		t.Fatalf("resolve runtime info: %v", err)
	}
	if info.Runtime.Database != "source_db" {
		t.Fatalf("Runtime.Database = %q, want %q", info.Runtime.Database, "source_db")
	}
	if !utils.PathsEqual(info.Runtime.BeadsDir, targetBeadsDir) {
		t.Fatalf("Runtime.BeadsDir = %q, want %q", info.Runtime.BeadsDir, targetBeadsDir)
	}
	if !utils.PathsEqual(info.Runtime.SourceBeadsDir, sourceBeadsDir) {
		t.Fatalf("Runtime.SourceBeadsDir = %q, want %q", info.Runtime.SourceBeadsDir, sourceBeadsDir)
	}
	if info.Config.GetDoltDatabase() != "target_db" {
		t.Fatalf("Config dolt_database = %q, want %q", info.Config.GetDoltDatabase(), "target_db")
	}
	if info.SourceConfig.GetDoltDatabase() != "source_db" {
		t.Fatalf("SourceConfig dolt_database = %q, want %q", info.SourceConfig.GetDoltDatabase(), "source_db")
	}
}
