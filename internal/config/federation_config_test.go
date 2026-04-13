package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fedTestHelper sets up a temp dir with config.yaml, chdir, and Initialize.
func fedTestHelper(t *testing.T, configContent string) {
	t.Helper()
	restore := envSnapshot(t)
	t.Cleanup(restore)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	if configContent != "" {
		configPath := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}
	}
	t.Chdir(tmpDir)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
}

func TestParseFederationConfig_Empty(t *testing.T) {
	fedTestHelper(t, "")

	cfg, err := ParseFederationConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Remote != "" {
		t.Errorf("Remote = %q, want empty", cfg.Remote)
	}
	if len(cfg.Remotes) != 0 {
		t.Errorf("Remotes = %v, want empty", cfg.Remotes)
	}
	if cfg.PrimaryRemote() != nil {
		t.Error("PrimaryRemote() should be nil for empty config")
	}
}

func TestParseFederationConfig_LegacyRemote(t *testing.T) {
	fedTestHelper(t, "federation:\n  remote: dolthub://myorg/beads\n  sovereignty: T2\n")

	cfg, err := ParseFederationConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Remote != "dolthub://myorg/beads" {
		t.Errorf("Remote = %q, want %q", cfg.Remote, "dolthub://myorg/beads")
	}
	if cfg.Sovereignty != SovereigntyT2 {
		t.Errorf("Sovereignty = %q, want %q", cfg.Sovereignty, SovereigntyT2)
	}
	if len(cfg.Remotes) != 1 {
		t.Fatalf("len(Remotes) = %d, want 1", len(cfg.Remotes))
	}
	r := cfg.Remotes[0]
	if r.Name != "origin" {
		t.Errorf("Remotes[0].Name = %q, want %q", r.Name, "origin")
	}
	if r.URL != "dolthub://myorg/beads" {
		t.Errorf("Remotes[0].URL = %q, want %q", r.URL, "dolthub://myorg/beads")
	}
	if r.Role != RemoteRolePrimary {
		t.Errorf("Remotes[0].Role = %q, want %q", r.Role, RemoteRolePrimary)
	}
}

func TestParseFederationConfig_NewRemotes(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    primary:
      url: dolthub://myorg/beads
      role: primary
    backup:
      url: az://account.blob.core.windows.net/container/beads
      role: backup
`)

	cfg, err := ParseFederationConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Remote != "dolthub://myorg/beads" {
		t.Errorf("Remote = %q, want %q", cfg.Remote, "dolthub://myorg/beads")
	}
	if len(cfg.Remotes) != 2 {
		t.Fatalf("len(Remotes) = %d, want 2", len(cfg.Remotes))
	}
	// Sorted by name: backup < primary
	if cfg.Remotes[0].Name != "backup" {
		t.Errorf("Remotes[0].Name = %q, want %q", cfg.Remotes[0].Name, "backup")
	}
	if cfg.Remotes[0].Role != RemoteRoleBackup {
		t.Errorf("Remotes[0].Role = %q, want %q", cfg.Remotes[0].Role, RemoteRoleBackup)
	}
	if cfg.Remotes[1].Name != "primary" {
		t.Errorf("Remotes[1].Name = %q, want %q", cfg.Remotes[1].Name, "primary")
	}
	if cfg.Remotes[1].Role != RemoteRolePrimary {
		t.Errorf("Remotes[1].Role = %q, want %q", cfg.Remotes[1].Role, RemoteRolePrimary)
	}

	// Helper methods
	p := cfg.PrimaryRemote()
	if p == nil {
		t.Fatal("PrimaryRemote() = nil, want non-nil")
	}
	if p.URL != "dolthub://myorg/beads" {
		t.Errorf("PrimaryRemote().URL = %q, want %q", p.URL, "dolthub://myorg/beads")
	}

	backups := cfg.BackupRemotes()
	if len(backups) != 1 {
		t.Fatalf("len(BackupRemotes()) = %d, want 1", len(backups))
	}
	if backups[0].Name != "backup" {
		t.Errorf("BackupRemotes()[0].Name = %q, want %q", backups[0].Name, "backup")
	}

	byName := cfg.RemoteByName("backup")
	if byName == nil || byName.URL != "az://account.blob.core.windows.net/container/beads" {
		t.Errorf("RemoteByName(backup) = %v, want az:// URL", byName)
	}
	if cfg.RemoteByName("nonexistent") != nil {
		t.Error("RemoteByName(nonexistent) should be nil")
	}
}

func TestParseFederationConfig_RemotesOverrideLegacy(t *testing.T) {
	var warningBuf strings.Builder
	oldWriter := ConfigWarningWriter
	ConfigWarningWriter = &warningBuf
	defer func() { ConfigWarningWriter = oldWriter }()

	fedTestHelper(t, `federation:
  remote: dolthub://old/beads
  remotes:
    main:
      url: dolthub://new/beads
      role: primary
`)

	cfg, err := ParseFederationConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Remote != "dolthub://new/beads" {
		t.Errorf("Remote = %q, want %q (from remotes primary)", cfg.Remote, "dolthub://new/beads")
	}
	if !strings.Contains(warningBuf.String(), "differs from") {
		t.Errorf("expected conflict warning, got: %q", warningBuf.String())
	}
}

func TestParseFederationConfig_NoPrimary(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    backup1:
      url: az://account.blob.core.windows.net/a/b
      role: backup
    backup2:
      url: gs://bucket/beads
      role: backup
`)

	_, err := ParseFederationConfig()
	if err == nil {
		t.Fatal("expected error for missing primary, got nil")
	}
	if !strings.Contains(err.Error(), `exactly one remote must have role "primary"`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseFederationConfig_MultiplePrimaries(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    one:
      url: dolthub://a/b
      role: primary
    two:
      url: dolthub://c/d
      role: primary
`)

	_, err := ParseFederationConfig()
	if err == nil {
		t.Fatal("expected error for multiple primaries, got nil")
	}
	if !strings.Contains(err.Error(), "found 2") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseFederationConfig_InvalidRole(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    main:
      url: dolthub://a/b
      role: unknown
`)

	_, err := ParseFederationConfig()
	if err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
	if !strings.Contains(err.Error(), "invalid role") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseFederationConfig_MissingURL(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    main:
      role: primary
`)

	_, err := ParseFederationConfig()
	if err == nil {
		t.Fatal("expected error for missing URL, got nil")
	}
	if !strings.Contains(err.Error(), "url is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseFederationConfig_MissingRole(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    main:
      url: dolthub://a/b
`)

	_, err := ParseFederationConfig()
	if err == nil {
		t.Fatal("expected error for missing role, got nil")
	}
	if !strings.Contains(err.Error(), "role is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseFederationConfig_InvalidRemoteName(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    "bad name!":
      url: dolthub://a/b
      role: primary
`)

	_, err := ParseFederationConfig()
	if err == nil {
		t.Fatal("expected error for invalid remote name, got nil")
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseFederationConfig_ArchiveRole(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    main:
      url: dolthub://org/beads
      role: primary
    cold:
      url: s3://archive-bucket/beads
      role: archive
`)

	cfg, err := ParseFederationConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Remotes) != 2 {
		t.Fatalf("len(Remotes) = %d, want 2", len(cfg.Remotes))
	}
	// Sorted: cold < main
	if cfg.Remotes[0].Role != RemoteRoleArchive {
		t.Errorf("Remotes[0].Role = %q, want archive", cfg.Remotes[0].Role)
	}
}

func TestParseFederationConfig_SynthesizesRuntimeRemote(t *testing.T) {
	fedTestHelper(t, `federation:
  remotes:
    main:
      url: dolthub://synthesized/beads
      role: primary
`)

	cfg, err := ParseFederationConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runtimeRemote := GetString("federation.remote")
	if runtimeRemote != "dolthub://synthesized/beads" {
		t.Errorf("runtime federation.remote = %q, want %q", runtimeRemote, "dolthub://synthesized/beads")
	}
	if cfg.Remote != runtimeRemote {
		t.Errorf("cfg.Remote = %q != runtime %q", cfg.Remote, runtimeRemote)
	}
}

func TestParseFederationConfig_LegacyGetFederationConfigCompat(t *testing.T) {
	// Existing GetFederationConfig should still work with legacy config
	fedTestHelper(t, "federation:\n  remote: dolthub://compat/beads\n  sovereignty: T1\n")

	cfg := GetFederationConfig()
	if cfg.Remote != "dolthub://compat/beads" {
		t.Errorf("GetFederationConfig().Remote = %q, want %q", cfg.Remote, "dolthub://compat/beads")
	}
	if cfg.Sovereignty != SovereigntyT1 {
		t.Errorf("GetFederationConfig().Sovereignty = %q, want %q", cfg.Sovereignty, SovereigntyT1)
	}
	if len(cfg.Remotes) != 1 {
		t.Fatalf("len(Remotes) = %d, want 1", len(cfg.Remotes))
	}
}

func TestValidateRemoteName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"primary", false},
		{"backup-1", false},
		{"my_remote", false},
		{"Remote2", false},
		{"", true},
		{"bad name", true},
		{"bad.name", true},
		{"bad/name", true},
		{strings.Repeat("a", 65), true},
	}
	for _, tc := range tests {
		err := validateRemoteName(tc.name)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateRemoteName(%q) err=%v, wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}

func TestRemoteRoleConstants(t *testing.T) {
	if RemoteRolePrimary != "primary" {
		t.Errorf("RemoteRolePrimary = %q", RemoteRolePrimary)
	}
	if RemoteRoleBackup != "backup" {
		t.Errorf("RemoteRoleBackup = %q", RemoteRoleBackup)
	}
	if RemoteRoleArchive != "archive" {
		t.Errorf("RemoteRoleArchive = %q", RemoteRoleArchive)
	}
}
