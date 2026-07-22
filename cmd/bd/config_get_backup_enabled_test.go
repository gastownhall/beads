package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

type backupEnabledEffectiveValueCase struct {
	name         string
	envVal       string // "\x00" = unset
	hasRemote    bool
	sharedServer bool
	wantPrefix   string // leading "true "/"false "
	wantContains string // source annotation substring
}

// TestConfigGetBackupEnabled_EffectiveValue pins wy-zrmqr fix #3: `bd config
// get backup.enabled` must report the EFFECTIVE value (what isBackupAutoEnabled
// actually returns) plus its source, not the raw stored value. Before the fix
// it printed "false"/"not set" even while auto-backup was running via
// primeHasGitRemote — the mismatch that hid the storm from operators.
func TestConfigGetBackupEnabled_EffectiveValue(t *testing.T) {
	runConfigGetBackupEnabledEffectiveValueCases(t, []backupEnabledEffectiveValueCase{
		{
			name:         "unset + remote + sql-server → off (server mode)",
			envVal:       "\x00",
			hasRemote:    true,
			sharedServer: true,
			wantPrefix:   "false ",
			wantContains: "sql-server mode",
		},
		{
			name:         "explicit true + sql-server → on (env var)",
			envVal:       "true",
			hasRemote:    false,
			sharedServer: true,
			wantPrefix:   "true ",
			wantContains: "env var",
		},
		{
			name:         "explicit false → off (env var)",
			envVal:       "false",
			hasRemote:    true,
			wantPrefix:   "false ",
			wantContains: "env var",
		},
	})
}

func runConfigGetBackupEnabledEffectiveValueCases(t *testing.T, tests []backupEnabledEffectiveValueCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := primeHasGitRemote
			primeHasGitRemote = func() bool { return tt.hasRemote }
			t.Cleanup(func() { primeHasGitRemote = orig })

			if tt.sharedServer {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			} else {
				os.Unsetenv("BEADS_DOLT_SHARED_SERVER")
				t.Cleanup(func() { os.Unsetenv("BEADS_DOLT_SHARED_SERVER") })
			}

			if tt.envVal == "\x00" {
				os.Unsetenv("BD_BACKUP_ENABLED")
				t.Cleanup(func() { os.Unsetenv("BD_BACKUP_ENABLED") })
			} else {
				t.Setenv("BD_BACKUP_ENABLED", tt.envVal)
			}

			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			oldJSON := jsonOutput
			jsonOutput = false
			t.Cleanup(func() { jsonOutput = oldJSON })

			out := captureStdout(t, runConfigGetBackupEnabled)

			if !strings.HasPrefix(out, tt.wantPrefix) {
				t.Errorf("output %q: want prefix %q", out, tt.wantPrefix)
			}
			if !strings.Contains(out, tt.wantContains) {
				t.Errorf("output %q: want to contain %q", out, tt.wantContains)
			}
		})
	}
}
