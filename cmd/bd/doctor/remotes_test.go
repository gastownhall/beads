package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestCheckRemoteConsistency_UsesRepoBeadsDir(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 3318,
		DoltDatabase:   "repo_db",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	check := CheckRemoteConsistency(repoDir)
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (message: %s)", check.Status, StatusWarning, check.Message)
	}
	if check.Message != "Could not query SQL remotes (server may not be running)" {
		t.Fatalf("message = %q", check.Message)
	}
}
