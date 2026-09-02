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
	"github.com/steveyegge/beads/internal/git"
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

func resetRepoContextCachesForTest(t *testing.T) {
	t.Helper()
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})
}

// setBeadsDirStartupProvenanceForTest pins whether this test simulates a
// caller who exported BEADS_DIR before bd started. The production value is
// captured once at process start, so tests must set it explicitly rather than
// inherit whatever environment the test process happened to launch with.
func setBeadsDirStartupProvenanceForTest(t *testing.T, provided bool) {
	t.Helper()
	old := beadsDirProvidedAtStartup
	beadsDirProvidedAtStartup = provided
	t.Cleanup(func() { beadsDirProvidedAtStartup = old })
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
	resetRepoContextCachesForTest(t)

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
	setBeadsDirStartupProvenanceForTest(t, true)

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

func TestActiveRepoPathForRoutingFallsBackToBeadsDirParent(t *testing.T) {
	resetRepoContextCachesForTest(t)

	// targetDir must sit outside any git repo, or beads.GetRepoContext()
	// succeeds against it and the FindBeadsDir fallback under test is never
	// reached. t.TempDir() resolves under os.TempDir(), which
	// internal/beads/context.go's isPathInSafeBoundary explicitly admits.
	targetDir := t.TempDir()
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")

	// The caller's CWD must also be outside any git repo, so
	// beads.GetRepoContext() can't resolve via CWD either.
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", targetBeadsDir)
	setBeadsDirStartupProvenanceForTest(t, true)

	// Compare through EvalSymlinks: on macOS t.TempDir() hands back a
	// /var/... symlink while resolution may canonicalize to /private/var/...
	// — the same directory. The sibling redirect tests already compare this
	// way; asserting the raw string makes the test order-fragile there.
	got := activeRepoPathForRouting()
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve got %q: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		t.Fatalf("resolve target %q: %v", targetDir, err)
	}
	if gotResolved != wantResolved {
		t.Fatalf("activeRepoPathForRouting() = %q (resolved %q), want %q", got, gotResolved, wantResolved)
	}
}

func TestActiveRepoPathForRoutingKeepsWorkspaceRepoAcrossRedirect(t *testing.T) {
	resetRepoContextCachesForTest(t)

	// A .beads/redirect relocates STORAGE, not the project: beads.role must
	// still come from the workspace repo the user is operating in, not from
	// wherever the redirect target lives (here: outside any git repo). Only
	// an explicit BEADS_DIR selection (bd -C) may move role detection.
	workspace := t.TempDir()
	initGitRepoForContextTest(t, workspace)
	storageDir := t.TempDir()
	storageBeadsDir := filepath.Join(storageDir, ".beads")
	writeTestConfigYAML(t, storageBeadsDir, "")

	workspaceBeadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(workspaceBeadsDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace beads dir: %v", err)
	}
	redirectFile := filepath.Join(workspaceBeadsDir, beads.RedirectFileName)
	if err := os.WriteFile(redirectFile, []byte(storageBeadsDir+"\n"), 0o600); err != nil {
		t.Fatalf("write redirect file: %v", err)
	}

	t.Chdir(workspace)
	t.Setenv("BEADS_DIR", "")
	setBeadsDirStartupProvenanceForTest(t, false)

	got := activeRepoPathForRouting()
	// Compare through EvalSymlinks: git resolves the physical path while
	// t.TempDir may hand back a symlinked one.
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve got %q: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace %q: %v", workspace, err)
	}
	if gotResolved != wantResolved {
		t.Fatalf("activeRepoPathForRouting() = %q (resolved %q), want workspace %q — a redirect must not move role detection to the storage root", got, gotResolved, wantResolved)
	}
}

func TestActiveRepoPathForRoutingSurvivesStartupRebindAcrossRedirect(t *testing.T) {
	resetRepoContextCachesForTest(t)

	// The regression this pins: rootCmd's PersistentPreRunE resolves the
	// redirect target and calls prepareSelectedCommandContext, which sets
	// BEADS_DIR for EVERY command — redirects included. Role detection must
	// not read that internal rebind as explicit user selection. The
	// helper-only redirect test above leaves BEADS_DIR empty and so never
	// exercises this startup path.
	workspace := t.TempDir()
	initGitRepoForContextTest(t, workspace)
	storageDir := t.TempDir()
	storageBeadsDir := filepath.Join(storageDir, ".beads")
	writeTestConfigYAML(t, storageBeadsDir, "")

	workspaceBeadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(workspaceBeadsDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace beads dir: %v", err)
	}
	redirectFile := filepath.Join(workspaceBeadsDir, beads.RedirectFileName)
	if err := os.WriteFile(redirectFile, []byte(storageBeadsDir+"\n"), 0o600); err != nil {
		t.Fatalf("write redirect file: %v", err)
	}

	t.Chdir(workspace)
	t.Setenv("BEADS_DIR", "")
	setBeadsDirStartupProvenanceForTest(t, false)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	oldServerMode := serverMode
	flagState := snapshotRootFlagState()
	t.Cleanup(func() {
		serverMode = oldServerMode
		restoreRootFlagState(t, flagState)
	})

	// The same rebind PersistentPreRunE performs once discovery has followed
	// the redirect to the storage .beads.
	prepareSelectedCommandContext(storageBeadsDir, false)

	if got := os.Getenv("BEADS_DIR"); got != storageBeadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q (startup rebind should have run)", got, storageBeadsDir)
	}

	got := activeRepoPathForRouting()
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve got %q: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace %q: %v", workspace, err)
	}
	if gotResolved != wantResolved {
		t.Fatalf("activeRepoPathForRouting() = %q (resolved %q), want workspace %q — the startup BEADS_DIR rebind must not turn a redirect into explicit selection", got, gotResolved, wantResolved)
	}
}

func TestActiveRepoPathForRoutingHonorsEnvFileSelection(t *testing.T) {
	resetRepoContextCachesForTest(t)

	// .beads/.env routing (loadSelectionEnvironment) is user-authored
	// selection: BEADS_DIR set there must keep binding role detection to the
	// selected project, exactly like exporting BEADS_DIR before running bd.
	callerDir := t.TempDir()
	initGitRepoForContextTest(t, callerDir)
	callerBeadsDir := filepath.Join(callerDir, ".beads")
	writeTestConfigYAML(t, callerBeadsDir, "")

	targetDir := t.TempDir()
	initGitRepoForContextTest(t, targetDir)
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")

	if err := os.WriteFile(filepath.Join(callerBeadsDir, ".env"), []byte("BEADS_DIR="+targetBeadsDir+"\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	t.Chdir(callerDir)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")
	setBeadsDirStartupProvenanceForTest(t, false)

	loadSelectionEnvironment()

	if got := os.Getenv("BEADS_DIR"); got != targetBeadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q (selection env load should have run)", got, targetBeadsDir)
	}

	got := activeRepoPathForRouting()
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve got %q: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		t.Fatalf("resolve target %q: %v", targetDir, err)
	}
	if gotResolved != wantResolved {
		t.Fatalf("activeRepoPathForRouting() = %q (resolved %q), want selected target %q — .env-provided BEADS_DIR is explicit selection", got, gotResolved, wantResolved)
	}
}

func TestActiveRepoPathForRoutingFallsBackToCurrentDirectory(t *testing.T) {
	resetRepoContextCachesForTest(t)

	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", "")
	setBeadsDirStartupProvenanceForTest(t, false)

	if got := activeRepoPathForRouting(); got != "." {
		t.Fatalf("activeRepoPathForRouting() = %q, want %q", got, ".")
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
