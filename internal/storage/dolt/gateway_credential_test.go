package dolt

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/endpointtrust"
	"github.com/steveyegge/beads/internal/testutil/credentialcmd"
)

// A configured credential command resolves the token into the username slot and marks
// the connection as targeting a gateway server (with auto-start disabled).
func TestApplyGatewayCredentialCommand(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Emit(t, "tok-abc"))
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
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Emit(t, `{"access_token":"tok-1","expires_in":300}`))
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
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Exit23(t))
	cfg := &Config{}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err == nil {
		t.Fatal("expected an error when the helper fails")
	}
	if !strings.Contains(err.Error(), "BEADS_DOLT_CREDENTIAL_COMMAND") {
		t.Fatalf("error should name the command var, got %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 23") {
		t.Fatalf("error should preserve helper exit status 23, got %v", err)
	}
	if applied || cfg.ServerUser != "" || cfg.Gateway {
		t.Fatalf("on failure the config must be untouched: %+v", cfg)
	}
}

// A caller/flag-preset ServerUser wins and the helper is never run.
func TestApplyGatewayCredentialPresetWins(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "credential-invoked")
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Marker(t, marker))
	cfg := &Config{ServerUser: "preset"}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err != nil {
		t.Fatalf("preset should short-circuit before running the helper: %v", err)
	}
	if applied || cfg.ServerUser != "preset" || cfg.Gateway {
		t.Fatalf("preset user must be preserved untouched: %+v", cfg)
	}
	credentialcmd.AssertMarkerAbsent(t, marker)
}

// A token containing a DSN-breaking character (: @ /) is refused, not mis-placed as
// username/password.
func TestApplyGatewayCredentialRejectsBadCharToken(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Emit(t, "tok@host"))
	cfg := &Config{}
	applied, err := ApplyGatewayCredential(context.Background(), &configfile.Config{}, cfg)
	if err == nil {
		t.Fatal("expected an error for a token with a DSN-breaking character")
	}
	if !strings.Contains(err.Error(), "contains a character") {
		t.Fatalf("error should report DSN character rejection, got %v", err)
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

func TestCheckServerEndpointTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	beadsDir := t.TempDir()

	// The file-config host is shared; a private env value confirms the pin.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "remote-a.example")
	cfg := &configfile.Config{DoltServerHost: "remote-a.example"}
	if err := CheckServerEndpointTrust(beadsDir, cfg, cfg.DoltServerHost, 3307, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	cfg.DoltServerHost = "remote-b.example"
	err := CheckServerEndpointTrust(beadsDir, cfg, cfg.DoltServerHost, 3307, "")
	if err == nil || !strings.Contains(err.Error(), "credential was not sent") {
		t.Fatalf("changed remote endpoint error = %v", err)
	}
	t.Setenv("BEADS_DOLT_SERVER_HOST", "remote-b.example")
	if err := CheckServerEndpointTrust(beadsDir, cfg, cfg.DoltServerHost, 3307, ""); err != nil {
		t.Fatalf("private environment confirmation: %v", err)
	}
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	for _, endpoint := range []struct {
		host, socket string
	}{
		{"127.0.0.1", ""},
		{"::1", ""},
		{"localhost", "/tmp/dolt.sock"},
	} {
		if err := CheckServerEndpointTrust(beadsDir, &configfile.Config{DoltServerHost: endpoint.host}, endpoint.host, 3307, endpoint.socket); err != nil {
			t.Errorf("local/socket endpoint %q %q: %v", endpoint.host, endpoint.socket, err)
		}
	}
}

func TestCheckServerEndpointTrustChecksTCPFallbackWithSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	beadsDir := t.TempDir()
	socket := filepath.Join(t.TempDir(), "dolt.sock")
	fileCfg := &configfile.Config{DoltServerHost: "remote-a.example"}

	// The file-config host is shared; a private env value confirms the pin.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "remote-a.example")
	if err := CheckServerEndpointTrust(beadsDir, fileCfg, fileCfg.DoltServerHost, 3307, socket); err != nil {
		t.Fatalf("initial socket and TCP endpoint: %v", err)
	}
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	fileCfg.DoltServerHost = "remote-b.example"
	err := CheckServerEndpointTrust(beadsDir, fileCfg, fileCfg.DoltServerHost, 3307, socket)
	var mismatch *endpointtrust.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("changed TCP fallback endpoint error = %v, want MismatchError", err)
	}
}

func TestApplyGatewayCredentialStopsBeforeCommandOnTrustMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	beadsDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "credential-invoked")
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Marker(t, marker))

	// The file-config host is shared; a private env value confirms the pin.
	t.Setenv("BEADS_DOLT_SERVER_HOST", "remote-a.example")
	fileCfg := &configfile.Config{DoltServerHost: "remote-a.example"}
	if err := CheckServerEndpointTrust(beadsDir, fileCfg, fileCfg.DoltServerHost, 3307, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	fileCfg.DoltServerHost = "remote-b.example"
	cfg := &Config{BeadsDir: beadsDir, ServerHost: fileCfg.DoltServerHost, ServerPort: 3307}
	applied, err := ApplyGatewayCredential(context.Background(), fileCfg, cfg)
	if err == nil || applied {
		t.Fatalf("applied=%v err=%v, expected trust failure", applied, err)
	}
	credentialcmd.AssertMarkerAbsent(t, marker)
}

// Through applyResolvedConfig: in server mode the command wins and marks Gateway; with
// no command the static user resolves; and the command is NOT run for an embedded store
// even when the env var is set (only a server presents a username).
func TestApplyResolvedConfigGatewayCredential(t *testing.T) {
	serverCfg := func() *configfile.Config {
		return &configfile.Config{DoltMode: configfile.DoltModeServer}
	}
	t.Run("server mode: command sets username + gateway", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Emit(t, "tok-xyz"))
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
		marker := filepath.Join(t.TempDir(), "credential-invoked")
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", credentialcmd.Marker(t, marker))
		cfg := &Config{}
		if err := applyResolvedConfig(context.Background(), t.TempDir(), &configfile.Config{}, cfg); err != nil {
			t.Fatalf("embedded open must not run the credential command: %v", err)
		}
		if cfg.Gateway {
			t.Fatal("embedded open must not be marked a gateway")
		}
		credentialcmd.AssertMarkerAbsent(t, marker)
	})
}
