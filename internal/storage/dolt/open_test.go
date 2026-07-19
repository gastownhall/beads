package dolt

import (
	"bytes"
	"context"
	"fmt"
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

func TestApplyCLIAutoStart_DisableAutoStartWins(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_TEST_MODE", "")

	beadsDir := t.TempDir()
	if err := configfile.DefaultConfig().Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	storeCfg := &Config{DisableAutoStart: true}
	ApplyCLIAutoStart(beadsDir, storeCfg)
	if storeCfg.AutoStart {
		t.Fatal("DisableAutoStart should suppress CLI auto-start defaults")
	}
}

func TestCLIDirUsesSharedDoltRootInSharedServerMode(t *testing.T) {
	sharedRoot := t.TempDir()
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

func TestApplyResolvedConfig_SetsCredentialCommand(t *testing.T) {
	const helper = "gasworks getToken beads --org o_1"

	// applyResolvedConfig now resolves the credential helper from trusted sources only
	// (BEADS_DOLT_CREDENTIAL_COMMAND env + central server config). Point the central path
	// at an absent file by default so a real ~/.config/beads/server.json on the host can
	// never leak into these tests; the central-path subtest overrides this with its own file.
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "absent-server.json"))

	t.Run("trusted env credential command threaded and eager token seeded", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", helper)
		stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"access_token":"eager-tok","expires_in":300}`), nil
		})
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_codex",
		}
		cfg := &Config{}

		if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
		if cfg.CredentialCommand != helper {
			t.Fatalf("CredentialCommand = %q, want %q", cfg.CredentialCommand, helper)
		}
		if cfg.ServerUser != "eager-tok" {
			t.Fatalf("ServerUser = %q, want the eager token %q", cfg.ServerUser, "eager-tok")
		}
	})

	t.Run("trusted central config credential command is honored", func(t *testing.T) {
		central := filepath.Join(t.TempDir(), "server.json")
		if err := os.WriteFile(central, []byte(fmt.Sprintf(`{"dolt_credential_command":%q}`, helper)), 0o600); err != nil {
			t.Fatalf("writing central config: %v", err)
		}
		t.Setenv("BEADS_CENTRAL_CONFIG", central)
		stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"access_token":"central-tok","expires_in":300}`), nil
		})
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_codex",
		}
		cfg := &Config{}

		if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
		if cfg.CredentialCommand != helper {
			t.Fatalf("CredentialCommand = %q, want %q", cfg.CredentialCommand, helper)
		}
		if cfg.ServerUser != "central-tok" {
			t.Fatalf("ServerUser = %q, want the central token %q", cfg.ServerUser, "central-tok")
		}
	})

	t.Run("tracked project metadata credential command is ignored, never executed", func(t *testing.T) {
		// Security regression: a repository-controlled .beads/metadata.json names a helper,
		// but with no trusted source configured bd must NOT run it. It falls back to the
		// static user and leaves cfg.CredentialCommand empty, so nothing reaches the sh -c
		// runner at open time or per dial.
		stubCredRunner(t, func(_ context.Context, cmd string) ([]byte, error) {
			t.Fatalf("credential helper from project metadata must never run, but ran %q", cmd)
			return nil, nil
		})
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:               configfile.BackendDolt,
			DoltMode:              configfile.DoltModeServer,
			DoltDatabase:          "beads_codex",
			DoltCredentialCommand: helper, // repo-controlled; must be ignored
		}
		cfg := &Config{}

		// Capture stderr to confirm the ignored-helper warning is surfaced.
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		err := applyResolvedConfig(beadsDir, fileCfg, cfg)

		w.Close()
		os.Stderr = oldStderr
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		if err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
		if cfg.CredentialCommand != "" {
			t.Fatalf("CredentialCommand = %q, want empty (metadata helper must not be adopted)", cfg.CredentialCommand)
		}
		if cfg.ServerUser != fileCfg.GetDoltServerUser() {
			t.Fatalf("ServerUser = %q, want the static fallback %q", cfg.ServerUser, fileCfg.GetDoltServerUser())
		}
		if !strings.Contains(buf.String(), "ignoring dolt_credential_command from project metadata.json") {
			t.Fatalf("expected an ignored-helper security warning on stderr, got: %q", buf.String())
		}
	})

	t.Run("explicit ServerUser bypasses the helper", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", helper)
		stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
			t.Fatal("helper must not run when ServerUser is pre-set")
			return nil, nil
		})
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_codex",
		}
		cfg := &Config{ServerUser: "preset"}

		if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
		if cfg.CredentialCommand != "" {
			t.Fatalf("CredentialCommand = %q, want empty (static path preserved)", cfg.CredentialCommand)
		}
		if cfg.ServerUser != "preset" {
			t.Fatalf("ServerUser = %q, want it left as %q", cfg.ServerUser, "preset")
		}
	})

	t.Run("failing trusted helper aborts store construction (fail-closed)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", helper)
		stubCredRunner(t, func(_ context.Context, _ string) ([]byte, error) {
			return nil, fmt.Errorf("mint denied")
		})
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			DoltDatabase: "beads_codex",
		}
		cfg := &Config{}

		err := applyResolvedConfig(beadsDir, fileCfg, cfg)
		if err == nil {
			t.Fatal("expected a fail-closed error when the helper fails")
		}
		if !strings.Contains(err.Error(), "resolving dolt credential command") {
			t.Fatalf("error = %q, want it to wrap %q", err.Error(), "resolving dolt credential command")
		}
	})
}

func TestApplyResolvedConfig(t *testing.T) {
	// applyResolvedConfig resolves the credential helper from the central server config when
	// no ServerUser is set; isolate it to an absent path so a host ~/.config/beads/server.json
	// cannot make these non-credential cases run a helper.
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "absent-server.json"))
	t.Run("fills server config for legacy metadata without dolt_mode", func(t *testing.T) {
		beadsDir := t.TempDir()
		fileCfg := &configfile.Config{
			Backend:      configfile.BackendDolt,
			DoltDatabase: "beads_codex",
			ProjectID:    "proj-123",
		}
		cfg := &Config{}

		if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

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

		if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

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

		if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

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

		if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}

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
