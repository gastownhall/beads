package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

func writeTestConfigYAML(t *testing.T, beadsDir, contents string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

type flagSnapshot struct {
	value   string
	changed bool
}

func snapshotRootFlagState() map[string]flagSnapshot {
	state := map[string]flagSnapshot{}
	for _, name := range []string{"db", "json", "format", "readonly", "actor", "dolt-auto-commit"} {
		flag := rootCmd.PersistentFlags().Lookup(name)
		if flag == nil {
			continue
		}
		state[name] = flagSnapshot{value: flag.Value.String(), changed: flag.Changed}
	}
	return state
}

func restoreRootFlagState(t *testing.T, state map[string]flagSnapshot) {
	t.Helper()
	for name, snapshot := range state {
		flag := rootCmd.PersistentFlags().Lookup(name)
		if flag == nil {
			continue
		}
		if err := flag.Value.Set(snapshot.value); err != nil {
			t.Fatalf("restore %s flag: %v", name, err)
		}
		flag.Changed = snapshot.changed
	}
}

func TestPrepareSelectedCommandContext_RebindsTargetConfig(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	callerDir := t.TempDir()
	callerBeadsDir := filepath.Join(callerDir, ".beads")
	writeTestConfigYAML(t, callerBeadsDir, "actor: caller-actor\ndolt.auto-start: true\ndolt.port: 1111\ndolt.auto-commit: on\n")

	targetDir := t.TempDir()
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "actor: target-actor\ndolt.auto-start: false\ndolt.port: 4242\ndolt.auto-commit: batch\njson: true\nreadonly: true\n")
	if err := (&configfile.Config{
		Backend:  configfile.BackendDolt,
		DoltMode: configfile.DoltModeServer,
	}).Save(targetBeadsDir); err != nil {
		t.Fatalf("save target metadata: %v", err)
	}

	t.Setenv("BEADS_DIR", callerBeadsDir)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	oldServerMode := serverMode
	oldJSONOutput := jsonOutput
	oldReadonlyMode := readonlyMode
	oldActor := actor
	oldDoltAutoCommit := doltAutoCommit
	flagState := snapshotRootFlagState()
	t.Cleanup(func() {
		serverMode = oldServerMode
		jsonOutput = oldJSONOutput
		readonlyMode = oldReadonlyMode
		actor = oldActor
		doltAutoCommit = oldDoltAutoCommit
		restoreRootFlagState(t, flagState)
	})

	serverMode = false
	jsonOutput = false
	readonlyMode = false
	actor = ""
	doltAutoCommit = ""
	for _, name := range []string{"json", "format", "readonly", "actor", "dolt-auto-commit"} {
		if flag := rootCmd.PersistentFlags().Lookup(name); flag != nil {
			flag.Changed = false
		}
	}

	prepareSelectedCommandContext(targetBeadsDir, false)
	refreshBoundCommandConfig(rootCmd)

	if got := os.Getenv("BEADS_DIR"); got != targetBeadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q", got, targetBeadsDir)
	}
	if !serverMode {
		t.Fatal("serverMode should be true after rebinding to target metadata")
	}
	if !jsonOutput {
		t.Fatal("jsonOutput should be rebound from target config")
	}
	if !readonlyMode {
		t.Fatal("readonlyMode should be rebound from target config")
	}
	if actor != "target-actor" {
		t.Fatalf("actor = %q, want %q", actor, "target-actor")
	}
	if doltAutoCommit != "batch" {
		t.Fatalf("doltAutoCommit = %q, want %q", doltAutoCommit, "batch")
	}
	if !doltserver.IsAutoStartDisabled() {
		t.Fatal("IsAutoStartDisabled should honor target config after rebinding")
	}
	if got := doltserver.DefaultConfig(targetBeadsDir).Port; got != 4242 {
		t.Fatalf("DefaultConfig(target).Port = %d, want %d", got, 4242)
	}
}

func TestPrepareSelectedCommandContext_DoesNotMergeCallerConfigForUnsetKeys(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	root := t.TempDir()
	callerDir := filepath.Join(root, "caller")
	callerBeadsDir := filepath.Join(callerDir, ".beads")
	writeTestConfigYAML(t, callerBeadsDir, "readonly: true\njson: true\n")

	targetDir := filepath.Join(root, "target")
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "actor: target-actor\n")

	t.Chdir(callerDir)
	t.Setenv("BEADS_DIR", callerBeadsDir)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	oldJSONOutput := jsonOutput
	oldReadonlyMode := readonlyMode
	oldActor := actor
	flagState := snapshotRootFlagState()
	t.Cleanup(func() {
		jsonOutput = oldJSONOutput
		readonlyMode = oldReadonlyMode
		actor = oldActor
		restoreRootFlagState(t, flagState)
	})

	jsonOutput = false
	readonlyMode = false
	actor = ""
	for _, name := range []string{"json", "format", "readonly", "actor"} {
		if flag := rootCmd.PersistentFlags().Lookup(name); flag != nil {
			flag.Changed = false
		}
	}

	prepareSelectedCommandContext(targetBeadsDir, false)
	refreshBoundCommandConfig(rootCmd)

	if readonlyMode {
		t.Fatal("readonlyMode should stay false when target config leaves readonly unset")
	}
	if jsonOutput {
		t.Fatal("jsonOutput should stay false when target config leaves json unset")
	}
	if actor != "target-actor" {
		t.Fatalf("actor = %q, want %q", actor, "target-actor")
	}
}
