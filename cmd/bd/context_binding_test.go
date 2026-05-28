package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/routing"
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

func initGitRepoForContextTest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	cmd = exec.Command("git", "config", "core.hooksPath", ".git/hooks")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config hooks: %v\n%s", err, output)
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

func TestDetectUserRoleForActiveRepoUsesSelectedBeadsDir(t *testing.T) {
	callerDir := t.TempDir()
	initGitRepoForContextTest(t, callerDir)

	targetDir := t.TempDir()
	initGitRepoForContextTest(t, targetDir)
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")

	cmd := exec.Command("git", "config", "beads.role", "maintainer")
	cmd.Dir = targetDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config beads.role: %v\n%s", err, output)
	}

	t.Chdir(callerDir)
	t.Setenv("BEADS_DIR", targetBeadsDir)
	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)

	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	oldStderr := os.Stderr
	os.Stderr = writeStderr
	t.Cleanup(func() {
		os.Stderr = oldStderr
		_ = readStderr.Close()
		_ = writeStderr.Close()
	})

	role, err := detectUserRoleForActiveRepo()
	_ = writeStderr.Close()
	stderrOutput, readErr := io.ReadAll(readStderr)
	if readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	if err != nil {
		t.Fatalf("detectUserRoleForActiveRepo: %v", err)
	}
	if role != routing.Maintainer {
		t.Fatalf("role = %q, want %q", role, routing.Maintainer)
	}
	if strings.Contains(string(stderrOutput), "beads.role not configured") {
		t.Fatalf("unexpected role warning from caller cwd:\n%s", stderrOutput)
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
