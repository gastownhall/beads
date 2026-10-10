package main

import (
	"os"
	"strings"
	"testing"
)

func TestCheckConfigSetSideEffects_FederationRemote(t *testing.T) {
	effects := checkConfigSetSideEffects("federation.remote", "dolthub://org/proj")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command == "" {
		t.Error("expected a suggested command")
	}
}

func TestCheckConfigSetSideEffects_SharedServerTrue(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.shared-server", "true")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt start" {
		t.Errorf("expected 'bd dolt start', got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_SharedServerFalse(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.shared-server", "false")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "" || !strings.Contains(effects[0].Message, "left running for other projects") {
		t.Errorf("shared disable must not blindly stop a shared process: %+v", effects[0])
	}
}

func TestCheckConfigSetSideEffects_DoltDebugTrue(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.debug", "true")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt restart" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_DoltDebugFalse(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.debug", "false")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt restart" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

func TestCheckConfigUnsetSideEffects_DoltDebug(t *testing.T) {
	effects := checkConfigUnsetSideEffects("dolt.debug")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt restart" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_DoltRemotesAPIPort(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.remotesapi-port", "8080")
	if len(effects) != 1 || effects[0].Command != "" ||
		!strings.Contains(effects[0].Message, "bd dolt restart") ||
		!strings.Contains(effects[0].Message, "shared-server-enabled workspace") {
		t.Fatalf("remotesapi side effect = %+v, want contextual fenced restart", effects)
	}
}

func TestCheckConfigSetSideEffects_RoutingModeInvalid(t *testing.T) {
	effects := checkConfigSetSideEffects("routing.mode", "bogus")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "" {
		t.Error("invalid routing mode should not suggest a command")
	}
}

func TestCheckConfigSetSideEffects_RoutingModeValid(t *testing.T) {
	for _, mode := range []string{"auto", "maintainer", "contributor", "explicit"} {
		effects := checkConfigSetSideEffects("routing.mode", mode)
		if len(effects) != 0 {
			t.Errorf("expected 0 effects for valid routing mode %q, got %d", mode, len(effects))
		}
	}
}

func TestCheckConfigSetSideEffects_BackupEnabled(t *testing.T) {
	effects := checkConfigSetSideEffects("backup.enabled", "true")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
}

func TestCheckConfigSetSideEffects_SyncGitRemote(t *testing.T) {
	effects := checkConfigSetSideEffects("sync.git-remote", "origin")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
}

func TestCheckConfigSetSideEffects_UnknownKey(t *testing.T) {
	effects := checkConfigSetSideEffects("some.random.key", "value")
	if len(effects) != 0 {
		t.Errorf("expected 0 effects for unknown key, got %d", len(effects))
	}
}

func TestCheckConfigUnsetSideEffects_FederationRemote(t *testing.T) {
	effects := checkConfigUnsetSideEffects("federation.remote")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt remote remove origin" {
		t.Errorf("expected 'bd dolt remote remove origin', got %q", effects[0].Command)
	}
}

func TestCheckConfigUnsetSideEffects_SharedServer(t *testing.T) {
	effects := checkConfigUnsetSideEffects("dolt.shared-server")
	if len(effects) != 1 || effects[0].Command != "" ||
		!strings.Contains(effects[0].Message, "left running for other projects") {
		t.Fatalf("shared unset must not blindly stop a shared process: %+v", effects)
	}
}

func TestCheckConfigUnsetSideEffects_DoltRemotesAPIPort(t *testing.T) {
	effects := checkConfigUnsetSideEffects("dolt.remotesapi-port")
	if len(effects) != 1 || effects[0].Command != "" ||
		!strings.Contains(effects[0].Message, "bd dolt restart") ||
		!strings.Contains(effects[0].Message, "shared-server-enabled workspace") {
		t.Fatalf("remotesapi unset side effect = %+v, want contextual fenced restart", effects)
	}
}

func TestCheckConfigUnsetSideEffects_BackupEnabled(t *testing.T) {
	effects := checkConfigUnsetSideEffects("backup.enabled")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
}

func TestCheckConfigUnsetSideEffects_UnknownKey(t *testing.T) {
	effects := checkConfigUnsetSideEffects("some.random.key")
	if len(effects) != 0 {
		t.Errorf("expected 0 effects for unknown key, got %d", len(effects))
	}
}

func TestPrintConfigSideEffects(t *testing.T) {
	// Redirect stderr to avoid test noise
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { w.Close(); r.Close(); os.Stderr = old }()

	// Should not panic with empty, single, or multiple effects
	printConfigSideEffects(nil)
	printConfigSideEffects([]configSideEffect{})
	printConfigSideEffects([]configSideEffect{
		{Message: "test hint", Command: "bd test"},
	})
	printConfigSideEffects([]configSideEffect{
		{Message: "no command hint"},
		{Message: "with command", Command: "bd apply"},
	})
}
