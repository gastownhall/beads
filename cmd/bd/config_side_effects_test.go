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
	if effects[0].Command != "bd dolt server start" {
		t.Errorf("expected 'bd dolt server start', got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_SharedServerFalse(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.shared-server", "false")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt server stop" {
		t.Errorf("expected 'bd dolt server stop', got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_DoltDebugTrue(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.debug", "true")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt stop && bd dolt start" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_DoltDebugFalse(t *testing.T) {
	effects := checkConfigSetSideEffects("dolt.debug", "false")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt stop && bd dolt start" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

func TestCheckConfigUnsetSideEffects_DoltDebug(t *testing.T) {
	effects := checkConfigUnsetSideEffects("dolt.debug")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt stop && bd dolt start" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
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

// versioned-history.enabled is a row of the store's config table, which
// replicates on `bd dolt push` and `bd dolt pull`, and the feature is single
// writer only. `bd config set ... true` is the moment someone turns it on, so it
// is where that caveat belongs -- said once, at the command that has the effect.
func TestVersionedHistoryConfigSetHintsAboutReplicationAndSingleWriter(t *testing.T) {
	for _, on := range []string{"true", "TRUE", " true ", "1", "t"} {
		effects := checkConfigSetSideEffects(versionedHistorySettingKey, on)
		if len(effects) != 1 {
			t.Errorf("value %q turns recording on: expected 1 hint, got %d", on, len(effects))
			continue
		}
		for _, want := range []string{"replicates", "single writer"} {
			if !strings.Contains(strings.ToLower(effects[0].Message), want) {
				t.Errorf("value %q: the hint does not mention %q: %s", on, want, effects[0].Message)
			}
		}
	}
	// Turning it off, or anything that does not turn it on, needs no caveat.
	for _, off := range []string{"false", "0", "", "off", "nonsense"} {
		if effects := checkConfigSetSideEffects(versionedHistorySettingKey, off); len(effects) != 0 {
			t.Errorf("value %q does not turn recording on: expected no hint, got %d", off, len(effects))
		}
	}
	if effects := checkConfigUnsetSideEffects(versionedHistorySettingKey); len(effects) != 0 {
		t.Errorf("unsetting the key needs no hint, got %d", len(effects))
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
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
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
