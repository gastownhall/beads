//go:build cgo

package main

import (
	"os"
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
