package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
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

	if out, err := exec.Command("git", "init", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
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
		ProjectID:      "proj-redir",
	}
	if err := targetCfg.Save(targetBeadsDir); err != nil {
		t.Fatalf("save target config: %v", err)
	}

	if err := os.WriteFile(filepath.Join(sourceBeadsDir, beads.RedirectFileName), []byte(targetBeadsDir+"\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	oldCwd, _ := os.Getwd()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	info := resolveRuntimeInfoForRepo(repoDir)
	if info.Runtime.Database != "source_db" {
		t.Fatalf("Runtime.Database = %q, want %q", info.Runtime.Database, "source_db")
	}
	if !info.Runtime.ServerMode {
		t.Fatal("expected runtime to report server mode")
	}
	if info.Runtime.Port != 3318 {
		t.Fatalf("Runtime.Port = %d, want %d", info.Runtime.Port, 3318)
	}
	if info.Config.ProjectID != "proj-redir" {
		t.Fatalf("Config.ProjectID = %q, want %q", info.Config.ProjectID, "proj-redir")
	}
}
