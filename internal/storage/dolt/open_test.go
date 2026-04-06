package dolt

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// TestResolveAutoStart verifies all conditions that govern the AutoStart decision.
//
// Each subtest uses t.Setenv for env-var isolation: t.Setenv records the
// original value (including the unset state) and restores it after the test,
// correctly handling cases where a variable was previously unset vs. set to "".
func TestResolveAutoStart(t *testing.T) {
	tests := []struct {
		name             string
		testMode         string // BEADS_TEST_MODE to set; "" leaves it unset/empty
		autoStartEnv     string // BEADS_DOLT_AUTO_START to set; "" leaves it unset/empty
		doltAutoStartCfg string // raw value of "dolt.auto-start" from config.yaml
		currentValue     bool   // AutoStart value supplied by caller
		wantAutoStart    bool
	}{
		{
			name:          "defaults to true for standalone user",
			wantAutoStart: true,
		},
		{
			name:          "disabled when BEADS_TEST_MODE=1",
			testMode:      "1",
			wantAutoStart: false,
		},
		{
			name:          "disabled when BEADS_DOLT_AUTO_START=0",
			autoStartEnv:  "0",
			wantAutoStart: false,
		},
		{
			name:          "enabled when BEADS_DOLT_AUTO_START=1",
			autoStartEnv:  "1",
			wantAutoStart: true,
		},
		{
			name:             "disabled when dolt.auto-start=false in config",
			doltAutoStartCfg: "false",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=0 in config",
			doltAutoStartCfg: "0",
			wantAutoStart:    false,
		},
		{
			name:             "disabled when dolt.auto-start=off in config",
			doltAutoStartCfg: "off",
			wantAutoStart:    false,
		},
		{
			name:          "test mode wins over BEADS_DOLT_AUTO_START=1",
			testMode:      "1",
			autoStartEnv:  "1",
			wantAutoStart: false,
		},
		{
			name:          "caller true preserved when no overrides",
			currentValue:  true,
			wantAutoStart: true,
		},
		{
			// Config opt-out must still win when callers pass current=true.
			name:             "config.yaml opt-out wins over caller true",
			currentValue:     true,
			doltAutoStartCfg: "false",
			wantAutoStart:    false,
		},
		{
			name:          "test mode overrides caller true",
			testMode:      "1",
			currentValue:  true,
			wantAutoStart: false,
		},
		{
			name:          "BEADS_DOLT_AUTO_START=0 overrides caller true",
			autoStartEnv:  "0",
			currentValue:  true,
			wantAutoStart: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", tc.testMode)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStartEnv)

			got := resolveAutoStart(tc.currentValue, tc.doltAutoStartCfg, ServerModeOwned)
			if got != tc.wantAutoStart {
				t.Errorf("resolveAutoStart(current=%v, configVal=%q, mode=Owned) = %v, want %v",
					tc.currentValue, tc.doltAutoStartCfg, got, tc.wantAutoStart)
			}
		})
	}
}

func TestApplyResolvedConfig(t *testing.T) {
	t.Run("fills server config for legacy metadata without dolt_mode", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltDatabase: "beads_codex",
			ProjectID:    "proj-123",
		}
		cfg := &Config{}

		applyResolvedConfig(beadsDir, fileCfg, cfg)

		if cfg.BeadsDir != beadsDir {
			t.Fatalf("BeadsDir = %q, want %q", cfg.BeadsDir, beadsDir)
		}
		if cfg.Path != fileCfg.DatabasePath(beadsDir) {
			t.Fatalf("Path = %q, want %q", cfg.Path, fileCfg.DatabasePath(beadsDir))
		}
		if cfg.Database != "beads_codex" {
			t.Fatalf("Database = %q, want beads_codex", cfg.Database)
		}
		if cfg.ServerHost != fileCfg.GetDoltServerHost() {
			t.Fatalf("ServerHost = %q, want %q", cfg.ServerHost, fileCfg.GetDoltServerHost())
		}
		if cfg.ServerUser != fileCfg.GetDoltServerUser() {
			t.Fatalf("ServerUser = %q, want %q", cfg.ServerUser, fileCfg.GetDoltServerUser())
		}
		wantPort := doltserver.DefaultConfig(beadsDir).Port
		if cfg.ServerPort != wantPort {
			t.Fatalf("ServerPort = %d, want %d", cfg.ServerPort, wantPort)
		}
	})

	t.Run("warns when data-dir set in server mode (GH#2438)", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_CodeWriter7",
			DoltDataDir:  "/some/stale/path",
		}
		cfg := &Config{}

		// Capture stderr to verify warning
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		applyResolvedConfig(beadsDir, fileCfg, cfg)

		w.Close()
		os.Stderr = oldStderr
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		output := buf.String()
		if !strings.Contains(output, "dolt_data_dir is set") {
			t.Errorf("expected data-dir warning in server mode, got: %q", output)
		}
		if !strings.Contains(output, "server mode") {
			t.Errorf("expected 'server mode' in warning, got: %q", output)
		}
	})

	t.Run("no warning when data-dir empty in server mode", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_CodeWriter7",
		}
		cfg := &Config{}

		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		applyResolvedConfig(beadsDir, fileCfg, cfg)

		w.Close()
		os.Stderr = oldStderr
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		if buf.Len() > 0 {
			t.Errorf("expected no warning when data-dir is empty, got: %q", buf.String())
		}
	})

	t.Run("preserves caller overrides", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltDatabase: "beads_codex",
		}
		cfg := &Config{
			BeadsDir:   "/override/.beads",
			Database:   "caller_db",
			ServerHost: "10.0.0.9",
			ServerPort: 15432,
			ServerUser: "custom",
		}

		applyResolvedConfig(beadsDir, fileCfg, cfg)

		if cfg.BeadsDir != "/override/.beads" {
			t.Fatalf("BeadsDir override lost: %q", cfg.BeadsDir)
		}
		if cfg.Database != "caller_db" {
			t.Fatalf("Database override lost: %q", cfg.Database)
		}
		if cfg.ServerHost != "10.0.0.9" {
			t.Fatalf("ServerHost override lost: %q", cfg.ServerHost)
		}
		if cfg.ServerPort != 15432 {
			t.Fatalf("ServerPort override lost: %d", cfg.ServerPort)
		}
		if cfg.ServerUser != "custom" {
			t.Fatalf("ServerUser override lost: %q", cfg.ServerUser)
		}
	})
}
