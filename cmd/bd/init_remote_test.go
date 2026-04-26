package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestInitCommandRegistersRemoteFlag(t *testing.T) {
	if initCmd.Flags().Lookup("remote") == nil {
		t.Fatal("init command does not register --remote")
	}
}

func TestInitExplicitRemoteDrivesCloneAndPersistence(t *testing.T) {
	const remote = "git+ssh://git@example.com/right/repo.git"
	resolveConfiguredRemote := func() string {
		return "git+ssh://git@example.com/wrong/repo.git"
	}

	syncURL, source := resolveInitConfiguredSyncRemote(remote, true, resolveConfiguredRemote)
	if syncURL != remote {
		t.Fatalf("syncURL = %q, want explicit remote %q", syncURL, remote)
	}
	if source != initSyncRemoteExplicit {
		t.Fatalf("source = %v, want explicit", source)
	}
	syncFromRemote := syncURL != ""
	syncURLFromConfig := syncURL != "" && source != initSyncRemoteNone
	if !syncFromRemote {
		t.Fatal("explicit remote did not select clone-from-remote path")
	}
	if !syncURLFromConfig {
		t.Fatal("explicit remote was not treated as user-configured sync URL")
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistInitSyncRemote(beadsDir, remote, syncURL, syncFromRemote, syncURLFromConfig); err != nil {
		t.Fatalf("persistInitSyncRemote failed: %v", err)
	}
	configBytes, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configBytes), remote) {
		t.Fatalf("config.yaml does not contain explicit remote %q:\n%s", remote, configBytes)
	}
}

func TestInitServerExternalRemoteUsesServerCloneMode(t *testing.T) {
	if got := initRemoteCloneMode(true, true); got != remoteCloneExternalServer {
		t.Fatalf("initRemoteCloneMode(server=true, external=true) = %v, want external server clone mode", got)
	}
}

func TestInitExplicitEmptyRemoteSkipsAmbientConfig(t *testing.T) {
	called := false
	resolveConfiguredRemote := func() string {
		called = true
		return "git+ssh://git@example.com/ambient/repo.git"
	}

	syncURL, source := resolveInitConfiguredSyncRemote("", true, resolveConfiguredRemote)
	if called {
		t.Fatal("explicit empty --remote consulted ambient sync config")
	}
	if syncURL != "" {
		t.Fatalf("syncURL = %q, want empty explicit remote", syncURL)
	}
	if source != initSyncRemoteExplicit {
		t.Fatalf("source = %v, want explicit", source)
	}
	if syncURL != "" {
		t.Fatal("explicit empty --remote would trigger early remote safety")
	}
}

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
