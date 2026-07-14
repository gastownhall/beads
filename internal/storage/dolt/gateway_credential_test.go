package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// A configured credential command resolves the token into the username slot and marks
// the connection as targeting a gateway server (with auto-start disabled).
func TestApplyGatewayCredentialCommand(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "printf tok-abc")
	cfg := &Config{}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if cfg.ServerUser != "tok-abc" {
		t.Fatalf("ServerUser = %q, want tok-abc", cfg.ServerUser)
	}
	if !cfg.Gateway || !cfg.DisableAutoStart {
		t.Fatalf("Gateway=%v DisableAutoStart=%v, want both true", cfg.Gateway, cfg.DisableAutoStart)
	}
}

// An ExecCredential/OAuth-style JSON envelope resolves the token.
func TestApplyGatewayCredentialJSONEnvelope(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", `printf '{"access_token":"tok-1","expires_in":300}'`)
	cfg := &Config{}
	if _, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ServerUser != "tok-1" {
		t.Fatalf("ServerUser = %q, want tok-1", cfg.ServerUser)
	}
}

// Fail-closed: a failing helper aborts and never leaves a fallback user.
func TestApplyGatewayCredentialFailsClosed(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "false")
	cfg := &Config{}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err == nil {
		t.Fatal("expected an error when the helper fails")
	}
	if !strings.Contains(err.Error(), "BEADS_DOLT_CREDENTIAL_COMMAND") {
		t.Fatalf("error should name the command var, got %v", err)
	}
	if applied || cfg.ServerUser != "" || cfg.Gateway {
		t.Fatalf("on failure the config must be untouched: %+v", cfg)
	}
}

// A caller/flag-preset ServerUser wins and the helper is never run (a failing command
// doubles as an exec detector — no error means it never executed).
func TestApplyGatewayCredentialPresetWins(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "false")
	cfg := &Config{ServerUser: "preset"}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err != nil {
		t.Fatalf("preset should short-circuit before running the helper: %v", err)
	}
	if applied || cfg.ServerUser != "preset" || cfg.Gateway {
		t.Fatalf("preset user must be preserved untouched: %+v", cfg)
	}
}

// A token containing a DSN-breaking character (: @ /) is refused, not mis-placed as
// username/password.
func TestApplyGatewayCredentialRejectsBadCharToken(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "printf tok@host")
	cfg := &Config{}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err == nil {
		t.Fatal("expected an error for a token with a DSN-breaking character")
	}
	if applied || cfg.ServerUser != "" || cfg.Gateway {
		t.Fatalf("config must be untouched on rejection: %+v", cfg)
	}
}

// Not configured: no-op, config untouched.
func TestApplyGatewayCredentialNotConfigured(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "")
	cfg := &Config{}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err != nil || applied {
		t.Fatalf("applied=%v err=%v, want (false,nil)", applied, err)
	}
	if cfg.Gateway || cfg.ServerUser != "" {
		t.Fatalf("config must be untouched when not configured: %+v", cfg)
	}
}

// Through applyResolvedConfig: in server mode the command wins and marks Gateway; with
// no command the static user resolves; and the command is NOT run for an embedded store
// even when the env var is set (only a server presents a username).
func TestApplyResolvedConfigGatewayCredential(t *testing.T) {
	serverCfg := func() *configfile.Config {
		return &configfile.Config{DoltMode: configfile.DoltModeServer}
	}
	t.Run("server mode: command sets username + gateway", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "printf tok-xyz")
		cfg := &Config{}
		if err := applyResolvedConfig(context.Background(), t.TempDir(), serverCfg(), cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.ServerUser != "tok-xyz" || !cfg.Gateway {
			t.Fatalf("ServerUser=%q Gateway=%v, want (tok-xyz,true)", cfg.ServerUser, cfg.Gateway)
		}
	})
	t.Run("server mode: no command falls back to static user", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "")
		cfg := &Config{}
		if err := applyResolvedConfig(context.Background(), t.TempDir(), serverCfg(), cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Gateway {
			t.Fatal("Gateway must be false with no command")
		}
	})
	t.Run("embedded mode: command is not run even when set", func(t *testing.T) {
		// Neutralize any ambient server-mode signal so an embedded metadata config really
		// resolves to embedded mode on this box/CI.
		t.Setenv("BEADS_DOLT_SERVER_MODE", "")
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		// A failing command doubles as an exec detector: if the gate let it run, the
		// open would error. It must not, because an embedded store presents no username.
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "false")
		cfg := &Config{}
		if err := applyResolvedConfig(context.Background(), t.TempDir(), &configfile.Config{}, cfg); err != nil {
			t.Fatalf("embedded open must not run the credential command: %v", err)
		}
		if cfg.Gateway {
			t.Fatal("embedded open must not be marked a gateway")
		}
	})
}
