package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestPersistInitSyncRemoteExplicitRemoteWritesTargetDir(t *testing.T) {
	tmpDir := t.TempDir()
	callerDir := filepath.Join(tmpDir, "caller")
	targetBeadsDir := filepath.Join(tmpDir, "target", ".beads")
	callerBeadsDir := filepath.Join(callerDir, ".beads")
	if err := os.MkdirAll(callerBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	callerConfig := filepath.Join(callerBeadsDir, "config.yaml")
	if err := os.WriteFile(callerConfig, []byte("sync.remote: git+ssh://git@example.com/wrong/repo.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	targetConfig := filepath.Join(targetBeadsDir, "config.yaml")
	if err := os.WriteFile(targetConfig, []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(callerDir); err != nil {
		t.Fatal(err)
	}

	const remote = "git+ssh://git@example.com/right/repo.git"
	if err := persistInitSyncRemote(targetBeadsDir, remote, remote, false, true); err != nil {
		t.Fatalf("persistInitSyncRemote failed: %v", err)
	}

	targetBytes, err := os.ReadFile(targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(targetBytes), remote) {
		t.Fatalf("target config.yaml does not contain explicit remote %q:\n%s", remote, targetBytes)
	}

	callerBytes, err := os.ReadFile(callerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(callerBytes), remote) {
		t.Fatalf("caller config.yaml was modified instead of target:\n%s", callerBytes)
	}
}

func TestInitTimeCloneConfigExternalDefaultsAreSelfContained(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_USER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")

	cfg := initTimeCloneConfig(true, "", 3312, "", "", "beads_proj")

	if cfg.GetDoltMode() != configfile.DoltModeServer {
		t.Fatalf("dolt mode = %q, want server", cfg.GetDoltMode())
	}
	if cfg.GetDoltServerHost() != configfile.DefaultDoltServerHost {
		t.Fatalf("host = %q, want default %q", cfg.GetDoltServerHost(), configfile.DefaultDoltServerHost)
	}
	if cfg.GetDoltServerUser() != configfile.DefaultDoltServerUser {
		t.Fatalf("user = %q, want default %q", cfg.GetDoltServerUser(), configfile.DefaultDoltServerUser)
	}
	if cfg.GetDoltServerPort() != 3312 {
		t.Fatalf("port = %d, want 3312", cfg.GetDoltServerPort())
	}
	if cfg.GetDoltDatabase() != "beads_proj" {
		t.Fatalf("database = %q, want beads_proj", cfg.GetDoltDatabase())
	}
}
