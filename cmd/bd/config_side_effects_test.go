package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// sideEffectStore returns a store directory whose config.yaml carries body
// (no config.yaml when body is ""); the store's own file decides Dolt
// ownership.
func sideEffectStore(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
	}
	return dir
}

func setEffects(t *testing.T, beadsDir, key, value string) []configSideEffect {
	t.Helper()
	effects, err := checkConfigSetSideEffects(beadsDir, key, value)
	if err != nil {
		t.Fatalf("checkConfigSetSideEffects(%q, %q): %v", key, value, err)
	}
	return effects
}

func unsetEffects(t *testing.T, beadsDir, key string) []configSideEffect {
	t.Helper()
	effects, err := checkConfigUnsetSideEffects(beadsDir, key)
	if err != nil {
		t.Fatalf("checkConfigUnsetSideEffects(%q): %v", key, err)
	}
	return effects
}

func TestCheckConfigSetSideEffects_FederationRemote(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "federation.remote", "dolthub://org/proj")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command == "" {
		t.Error("expected a suggested command")
	}
}

func TestCheckConfigSetSideEffects_SharedServerTrue(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "dolt.shared-server", "true")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt server start" {
		t.Errorf("expected 'bd dolt server start', got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_SharedServerFalse(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "dolt.shared-server", "false")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt server stop" {
		t.Errorf("expected 'bd dolt server stop', got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_DoltDebugTrue(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "dolt.debug", "true")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt stop && bd dolt start" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

func TestCheckConfigSetSideEffects_DoltDebugFalse(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "dolt.debug", "false")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt stop && bd dolt start" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

func TestCheckConfigUnsetSideEffects_DoltDebug(t *testing.T) {
	effects := unsetEffects(t, sideEffectStore(t, ""), "dolt.debug")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt stop && bd dolt start" {
		t.Errorf("expected restart command, got %q", effects[0].Command)
	}
}

// TestConfigSideEffects_DoltDebugOnCityOwnedStore proves the restart hint
// follows the store's Gas City ownership stamp.
func TestConfigSideEffects_DoltDebugOnCityOwnedStore(t *testing.T) {
	dir := sideEffectStore(t, config.GasCityEndpointOriginKey+": "+string(config.GasCityOriginInheritedCity)+"\n")
	for _, effects := range [][]configSideEffect{
		setEffects(t, dir, "dolt.debug", "true"),
		setEffects(t, dir, "dolt.debug", "false"),
		unsetEffects(t, dir, "dolt.debug"),
	} {
		if len(effects) != 1 || effects[0].Command != "gc stop && gc start" {
			t.Fatalf("effects = %+v, want one restart hint naming gc", effects)
		}
	}
}

// TestConfigSideEffects_DoltDebugInvalidOwnershipIsAnError proves an unknown
// gc.endpoint_origin fails the hint instead of printing the bd restart.
func TestConfigSideEffects_DoltDebugInvalidOwnershipIsAnError(t *testing.T) {
	dir := sideEffectStore(t, config.GasCityEndpointOriginKey+": somebody_else\n")
	if effects, err := checkConfigSetSideEffects(dir, "dolt.debug", "true"); err == nil || !strings.Contains(err.Error(), "somebody_else") {
		t.Fatalf("checkConfigSetSideEffects = (%+v, %v), want the invalid-origin error", effects, err)
	}
	if effects, err := checkConfigUnsetSideEffects(dir, "dolt.debug"); err == nil || !strings.Contains(err.Error(), "somebody_else") {
		t.Fatalf("checkConfigUnsetSideEffects = (%+v, %v), want the invalid-origin error", effects, err)
	}
}

func TestCheckConfigSetSideEffects_RoutingModeInvalid(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "routing.mode", "bogus")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "" {
		t.Error("invalid routing mode should not suggest a command")
	}
}

func TestCheckConfigSetSideEffects_RoutingModeValid(t *testing.T) {
	dir := sideEffectStore(t, "")
	for _, mode := range []string{"auto", "maintainer", "contributor", "explicit"} {
		effects := setEffects(t, dir, "routing.mode", mode)
		if len(effects) != 0 {
			t.Errorf("expected 0 effects for valid routing mode %q, got %d", mode, len(effects))
		}
	}
}

func TestCheckConfigSetSideEffects_BackupEnabled(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "backup.enabled", "true")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
}

func TestCheckConfigSetSideEffects_SyncGitRemote(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "sync.git-remote", "origin")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
}

func TestCheckConfigSetSideEffects_UnknownKey(t *testing.T) {
	effects := setEffects(t, sideEffectStore(t, ""), "some.random.key", "value")
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
	effects := unsetEffects(t, sideEffectStore(t, ""), "federation.remote")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
	if effects[0].Command != "bd dolt remote remove origin" {
		t.Errorf("expected 'bd dolt remote remove origin', got %q", effects[0].Command)
	}
}

func TestCheckConfigUnsetSideEffects_SharedServer(t *testing.T) {
	effects := unsetEffects(t, sideEffectStore(t, ""), "dolt.shared-server")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
}

func TestCheckConfigUnsetSideEffects_BackupEnabled(t *testing.T) {
	effects := unsetEffects(t, sideEffectStore(t, ""), "backup.enabled")
	if len(effects) != 1 {
		t.Fatalf("expected 1 effect, got %d", len(effects))
	}
}

func TestCheckConfigUnsetSideEffects_UnknownKey(t *testing.T) {
	effects := unsetEffects(t, sideEffectStore(t, ""), "some.random.key")
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
