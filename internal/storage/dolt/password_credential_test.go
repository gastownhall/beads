package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// A configured secret command resolves the token into the password slot.
func TestApplyPasswordCommand(t *testing.T) {
	t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", "printf s3cr3t")
	cfg := &Config{}
	applied, err := ApplyPasswordCommand(context.Background(), &configfile.Config{}, cfg)
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if cfg.ServerPassword != "s3cr3t" {
		t.Fatalf("ServerPassword = %q, want s3cr3t", cfg.ServerPassword)
	}
}

// An ExecCredential/OAuth-style JSON envelope resolves the token.
func TestApplyPasswordCommandJSONEnvelope(t *testing.T) {
	t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", `printf '{"access_token":"pw-1","expires_in":300}'`)
	cfg := &Config{}
	if _, err := ApplyPasswordCommand(context.Background(), &configfile.Config{}, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ServerPassword != "pw-1" {
		t.Fatalf("ServerPassword = %q, want pw-1", cfg.ServerPassword)
	}
}

// Fail-closed: a failing helper aborts and never leaves a fallback password.
func TestApplyPasswordCommandFailsClosed(t *testing.T) {
	t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", "false")
	cfg := &Config{}
	applied, err := ApplyPasswordCommand(context.Background(), &configfile.Config{}, cfg)
	if err == nil {
		t.Fatal("expected an error when the helper fails")
	}
	if !strings.Contains(err.Error(), "BEADS_DOLT_PASSWORD_COMMAND") {
		t.Fatalf("error should name the command var, got %v", err)
	}
	if applied || cfg.ServerPassword != "" {
		t.Fatalf("on failure the config must be untouched: %+v", cfg)
	}
}

// A caller/flag-preset ServerPassword wins and the helper is never run (a failing
// command doubles as an exec detector — no error means it never executed).
func TestApplyPasswordCommandPresetWins(t *testing.T) {
	t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", "false")
	cfg := &Config{ServerPassword: "preset"}
	applied, err := ApplyPasswordCommand(context.Background(), &configfile.Config{}, cfg)
	if err != nil {
		t.Fatalf("preset should short-circuit before running the helper: %v", err)
	}
	if applied || cfg.ServerPassword != "preset" {
		t.Fatalf("preset password must be preserved untouched: %+v", cfg)
	}
}

// Not configured: no-op, config untouched.
func TestApplyPasswordCommandNotConfigured(t *testing.T) {
	t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", "")
	cfg := &Config{}
	applied, err := ApplyPasswordCommand(context.Background(), &configfile.Config{}, cfg)
	if err != nil || applied {
		t.Fatalf("applied=%v err=%v, want (false,nil)", applied, err)
	}
	if cfg.ServerPassword != "" {
		t.Fatalf("config must be untouched when not configured: %+v", cfg)
	}
}

// Through applyResolvedConfig: with no BEADS_DOLT_PASSWORD env var and no
// credentials file (INI absent), a configured BEADS_DOLT_PASSWORD_COMMAND
// resolves the connection password — proving bd can get its password from the
// command alone. It also runs regardless of dolt_mode (unlike the identity
// command, GetDoltServerPasswordForPort's static tier already applies
// unconditionally, so the command rung mirrors that scope).
func TestApplyResolvedConfigPasswordCommand(t *testing.T) {
	t.Run("command resolves the password with no env var and no credentials file", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", "printf hunter2")
		t.Setenv("BEADS_DOLT_PASSWORD", "")
		t.Setenv("BEADS_CREDENTIALS_FILE", "/nonexistent/credentials")
		cfg := &Config{}
		if err := applyResolvedConfig(context.Background(), t.TempDir(), &configfile.Config{}, cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.ServerPassword != "hunter2" {
			t.Fatalf("ServerPassword = %q, want hunter2", cfg.ServerPassword)
		}
	})
	t.Run("no command falls back to the static BEADS_DOLT_PASSWORD tier", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", "")
		t.Setenv("BEADS_DOLT_PASSWORD", "static-pw")
		cfg := &Config{}
		if err := applyResolvedConfig(context.Background(), t.TempDir(), &configfile.Config{}, cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.ServerPassword != "static-pw" {
			t.Fatalf("ServerPassword = %q, want static-pw", cfg.ServerPassword)
		}
	})
	t.Run("a failing command aborts the open instead of falling back to the static tier", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_PASSWORD_COMMAND", "false")
		t.Setenv("BEADS_DOLT_PASSWORD", "static-pw")
		cfg := &Config{}
		err := applyResolvedConfig(context.Background(), t.TempDir(), &configfile.Config{}, cfg)
		if err == nil {
			t.Fatal("expected an error when the command fails")
		}
		if cfg.ServerPassword != "" {
			t.Fatalf("on failure the config must be untouched, got ServerPassword=%q", cfg.ServerPassword)
		}
	})
}
