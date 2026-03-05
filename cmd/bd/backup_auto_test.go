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
		envVal     string // "" = not set (use default), "true"/"false" = explicit
		hasRemote  bool
		wantResult bool
	}{
		{
			name:       "default + git remote → enabled",
			envVal:     "",
			hasRemote:  true,
			wantResult: true,
		},
		{
			name:       "default + no git remote → disabled",
			envVal:     "",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Stub primeHasGitRemote
			orig := primeHasGitRemote
			primeHasGitRemote = func() bool { return tt.hasRemote }
			t.Cleanup(func() { primeHasGitRemote = orig })

			// Set env var if needed (simulates explicit config)
			if tt.envVal != "" {
				t.Setenv("BD_BACKUP_ENABLED", tt.envVal)
			} else {
				os.Unsetenv("BD_BACKUP_ENABLED")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_ENABLED") })
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

func TestIsBackupGitPushEnabled(t *testing.T) {
	// Cannot be parallel: modifies global primeHasGitRemote and env vars.

	tests := []struct {
		name       string
		envVal     string // "" = not set (use default), "true"/"false" = explicit
		hasRemote  bool
		noGitOps   bool // simulate stealth mode
		wantResult bool
	}{
		{
			name:       "default + git remote → enabled (follows auto-enable)",
			envVal:     "",
			hasRemote:  true,
			wantResult: true,
		},
		{
			name:       "default + no remote → disabled",
			envVal:     "",
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
			name:       "stealth mode overrides default + remote",
			envVal:     "",
			hasRemote:  true,
			noGitOps:   true,
			wantResult: false,
		},
		{
			name:       "stealth mode overrides explicit true",
			envVal:     "true",
			hasRemote:  true,
			noGitOps:   true,
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Stub primeHasGitRemote
			orig := primeHasGitRemote
			primeHasGitRemote = func() bool { return tt.hasRemote }
			t.Cleanup(func() { primeHasGitRemote = orig })

			// Set env var if needed (simulates explicit config)
			if tt.envVal != "" {
				t.Setenv("BD_BACKUP_GIT_PUSH", tt.envVal)
			} else {
				os.Unsetenv("BD_BACKUP_GIT_PUSH")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_GIT_PUSH") })
			}
			// Also clear the backup.enabled env to not interfere
			os.Unsetenv("BD_BACKUP_ENABLED")

			// Stealth mode
			if tt.noGitOps {
				t.Setenv("BD_NO_GIT_OPS", "true")
			} else {
				os.Unsetenv("BD_NO_GIT_OPS")
			}

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			got := isBackupGitPushEnabled()
			if got != tt.wantResult {
				t.Errorf("isBackupGitPushEnabled() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

func TestIsDefaultBranch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"master", true},
		{"feature/my-work", false},
		{"fix/backup-bug", false},
		{"develop", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			t.Parallel()
			if got := isDefaultBranch(tt.branch); got != tt.want {
				t.Errorf("isDefaultBranch(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}
