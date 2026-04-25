package dolt

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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

// TestApplyCLIAutoStart_RespectsExternalMode verifies that an external-mode
// repo (metadata.json with explicit dolt_server_port) suppresses the CLI
// auto-start path, preventing the shadow-database fallback when the
// configured external server is transiently unreachable.
//
// Regression for the case where ApplyCLIAutoStart hardcoded ServerModeOwned
// and bypassed resolveAutoStart's external-mode check, so any bd command in
// an external-mode repo could spawn a fallback embedded server when the
// configured server didn't respond (e.g. during a service cutover).
func TestApplyCLIAutoStart_RespectsExternalMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_TEST_MODE", "")

	beadsDir := t.TempDir()
	// metadata.json with explicit dolt_server_port → ServerModeExternal.
	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerPort = 3399
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	if got := doltserver.ResolveServerMode(beadsDir); got != doltserver.ServerModeExternal {
		t.Fatalf("ResolveServerMode = %v, want External (preflight)", got)
	}

	// No config.yaml dolt.auto-start set — relies entirely on External-
	// mode suppression. Pre-fix this would return AutoStart=true (default).
	storeCfg := &Config{}
	ApplyCLIAutoStart(beadsDir, storeCfg)
	if storeCfg.AutoStart {
		t.Errorf("ApplyCLIAutoStart set AutoStart=true in external-mode repo; want false (shadow database protection)")
	}
}

func TestCLIDirUsesSharedDoltRootInSharedServerMode(t *testing.T) {
	sharedRoot := t.TempDir()
	t.Setenv(EnvDoltCLIDir, "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedRoot)

	store := &DoltStore{
		serverMode: true,
		beadsDir:   filepath.Join(t.TempDir(), ".beads"),
		dbPath:     filepath.Join(t.TempDir(), ".beads", "dolt"),
		database:   "shared_db",
	}

	want := filepath.Join(sharedRoot, "dolt", "shared_db")
	if got := store.CLIDir(); got != want {
		t.Fatalf("CLIDir() = %q, want %q", got, want)
	}
}

func TestCLIDirUsesDbPathOutsideSharedServerMode(t *testing.T) {
	t.Setenv(EnvDoltCLIDir, "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")

	dbPath := filepath.Join(t.TempDir(), ".beads", "dolt")
	store := &DoltStore{
		serverMode: true,
		beadsDir:   filepath.Join(t.TempDir(), ".beads"),
		dbPath:     dbPath,
		database:   "local_db",
	}

	want := filepath.Join(dbPath, "local_db")
	if got := store.CLIDir(); got != want {
		t.Fatalf("CLIDir() = %q, want %q", got, want)
	}
}

func TestCLIDirUsesExplicitEnvOverride(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	cliDir := filepath.Join(t.TempDir(), "server-db")
	t.Setenv(EnvDoltCLIDir, cliDir)

	store := &DoltStore{
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
		dbPath:      filepath.Join(t.TempDir(), ".beads", "dolt"),
		database:    "local_db",
	}

	if got := store.CLIDir(); got != cliDir {
		t.Fatalf("CLIDir() = %q, want %q", got, cliDir)
	}
}

func TestCLIDirEmptyForGenericExternalServerModeWithoutEnv(t *testing.T) {
	t.Setenv(EnvDoltCLIDir, "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")

	store := &DoltStore{
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
		dbPath:      filepath.Join(t.TempDir(), ".beads", "dolt"),
		database:    "local_db",
	}

	if got := store.CLIDir(); got != "" {
		t.Fatalf("CLIDir() = %q, want empty string", got)
	}
}

func TestDoltCLIRequiresExplicitDirInGenericExternalServerMode(t *testing.T) {
	t.Setenv(EnvDoltCLIDir, "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")

	store := &DoltStore{
		serverMode:  true,
		serverOwner: doltserver.ServerModeExternal,
		branch:      "main",
	}

	err := store.doltCLIPull(t.Context(), "origin", nil)
	if err == nil {
		t.Fatal("doltCLIPull() error = nil, want explicit CLI dir error")
	}
	if !strings.Contains(err.Error(), EnvDoltCLIDir) {
		t.Fatalf("doltCLIPull() error = %q, want mention of %s", err.Error(), EnvDoltCLIDir)
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
