//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestIsBackupAutoEnabled(t *testing.T) {
	// Cannot be parallel: modifies global primeHasGitRemote and env vars.

	tests := []struct {
		name       string
		envVal     string // "\x00" = not set, "" = set to empty, "true"/"false"/"0" = explicit
		hasRemote  bool
		wantResult bool
	}{
		{
			name:       "default + git remote → enabled",
			envVal:     "\x00",
			hasRemote:  true,
			wantResult: true,
		},
		{
			name:       "default + no git remote → disabled",
			envVal:     "\x00",
			hasRemote:  false,
			wantResult: false,
		},
		{
			name:       "explicit true + no remote → enabled",
			envVal:     "true",
			hasRemote:  false,
			wantResult: true,
		},
		{
			name:       "explicit false + remote → disabled",
			envVal:     "false",
			hasRemote:  true,
			wantResult: false,
		},
		{
			name:       "explicit 0 + remote → disabled",
			envVal:     "0",
			hasRemote:  true,
			wantResult: false,
		},
		{
			name:       "empty string + remote → disabled (env set = explicit)",
			envVal:     "",
			hasRemote:  true,
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Stub primeHasGitRemote
			orig := primeHasGitRemote
			primeHasGitRemote = func() bool { return tt.hasRemote }
			t.Cleanup(func() { primeHasGitRemote = orig })

			// Set env var: "\x00" = unset, anything else = set to that value
			if tt.envVal == "\x00" {
				os.Unsetenv("BD_BACKUP_ENABLED")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_ENABLED") })
			} else {
				t.Setenv("BD_BACKUP_ENABLED", tt.envVal)
			}

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			got := isBackupAutoEnabled()
			if got != tt.wantResult {
				t.Errorf("isBackupAutoEnabled() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

// TestClientServerShareFilesystem_GH3523 pins the gating logic that
// suppresses auto-backup's file:// register when the Dolt server
// runs on a different filesystem from the client. Pre-fix every
// command emitted "auto-backup failed: register backup remote: ...
// failed to create directory ..." for operators with an external
// (non-localhost) Dolt server.
func TestClientServerShareFilesystem_GH3523(t *testing.T) {
	tests := []struct {
		name      string
		envHost   string // "\x00" = unset
		yamlHost  string // "\x00" = unset
		wantShare bool
	}{
		{
			name:      "no env, no yaml → embedded/local, share=true",
			envHost:   "\x00",
			yamlHost:  "\x00",
			wantShare: true,
		},
		{
			name:      "env=localhost → local, share=true",
			envHost:   "localhost",
			yamlHost:  "\x00",
			wantShare: true,
		},
		{
			name:      "env=127.0.0.1 → local, share=true",
			envHost:   "127.0.0.1",
			yamlHost:  "\x00",
			wantShare: true,
		},
		{
			name:      "env=non-localhost IP → external, share=false",
			envHost:   "192.0.2.10",
			yamlHost:  "\x00",
			wantShare: false,
		},
		{
			name:      "env=non-localhost FQDN → external, share=false",
			envHost:   "dolt-primary.tailnet.example.com",
			yamlHost:  "\x00",
			wantShare: false,
		},
		{
			name:      "yaml dolt.host=non-localhost → external, share=false",
			envHost:   "\x00",
			yamlHost:  "10.0.0.5",
			wantShare: false,
		},
		{
			name:      "env=empty (set to empty), yaml=non-localhost → external, share=false",
			envHost:   "",
			yamlHost:  "10.0.0.5",
			wantShare: false,
		},
		{
			name:      "env=localhost overrides yaml=non-localhost → share=true",
			envHost:   "localhost",
			yamlHost:  "10.0.0.5",
			wantShare: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envHost == "\x00" {
				os.Unsetenv("BEADS_DOLT_SERVER_HOST")
			} else {
				t.Setenv("BEADS_DOLT_SERVER_HOST", tt.envHost)
			}

			// Construct a fresh config.yaml for the dolt.host case.
			configDir := t.TempDir()
			if tt.yamlHost != "\x00" {
				yamlPath := filepath.Join(configDir, "config.yaml")
				content := "dolt:\n  host: " + tt.yamlHost + "\n"
				if err := os.WriteFile(yamlPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BEADS_DIR", configDir)
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			got := clientServerShareFilesystem()
			if got != tt.wantShare {
				t.Errorf("clientServerShareFilesystem() = %v, want %v", got, tt.wantShare)
			}
		})
	}
}
