package main

import (
	"context"
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

// clearActorEnv blanks the two environment variables that outrank a
// config.yaml actor, so a test asserting the config value is rebound does not
// depend on the developer's shell (GH#6560). BEADS_ACTOR outranks it
// deliberately: resolveConfiguredActor checks the env first, which is the
// GH#4645 fix. BD_ACTOR does too, because viper's BD-prefixed AutomaticEnv
// reads it as the actor key ahead of the file. CI runners set neither, which is
// why these tests only ever failed locally.
func clearActorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("BD_ACTOR", "")
}

func TestPrepareSelectedCommandContext_RebindsTargetConfig(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	clearActorEnv(t)

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

// newRedirectedWorkspaceForTest builds the explicit x redirect fixture: a git
// workspace whose .beads holds only a redirect, pointing at a SEPARATE git
// repo's .beads that holds the project files. Two distinct repos is what makes
// buildRepoContext treat the context as external, so rc.RepoRoot really is the
// storage root and a wrong answer is visible instead of coincidentally right.
func newRedirectedWorkspaceForTest(t *testing.T) (workspace, storageBeadsDir string) {
	t.Helper()

	workspace = filepath.Join(t.TempDir(), "workspace")
	initGitRepoForContextTest(t, workspace)
	storageBeadsDir = newStorageBeadsDirForTest(t)
	writeRedirectForTest(t, filepath.Join(workspace, ".beads"), storageBeadsDir)
	return workspace, storageBeadsDir
}

// newStorageBeadsDirForTest returns the .beads of a fresh git repo that holds
// the project files, for a redirect fixture to point at.
func newStorageBeadsDirForTest(t *testing.T) string {
	t.Helper()

	storage := filepath.Join(t.TempDir(), "storage")
	initGitRepoForContextTest(t, storage)
	storageBeadsDir := filepath.Join(storage, ".beads")
	writeTestConfigYAML(t, storageBeadsDir, "")
	return storageBeadsDir
}

// writeRedirectForTest makes beadsDir a .beads that holds only a redirect to
// target.
func writeRedirectForTest(t *testing.T, beadsDir, target string) {
	t.Helper()

	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir redirect beads dir: %v", err)
	}
	redirectFile := filepath.Join(beadsDir, beads.RedirectFileName)
	if err := os.WriteFile(redirectFile, []byte(target+"\n"), 0o600); err != nil {
		t.Fatalf("write redirect file: %v", err)
	}
}

// newNestedRedirectedWorkspaceForTest builds a redirected workspace one
// directory below its git repo root, and the root holds no .beads, so discovery
// reaches the redirect only by walking up from the CWD.
func newNestedRedirectedWorkspaceForTest(t *testing.T) (cwd, repoRoot, redirectBeadsDir, storageBeadsDir string) {
	t.Helper()

	repoRoot = filepath.Join(t.TempDir(), "repo")
	initGitRepoForContextTest(t, repoRoot)
	cwd = filepath.Join(repoRoot, "sub")
	storageBeadsDir = newStorageBeadsDirForTest(t)
	redirectBeadsDir = filepath.Join(cwd, ".beads")
	writeRedirectForTest(t, redirectBeadsDir, storageBeadsDir)
	return cwd, repoRoot, redirectBeadsDir, storageBeadsDir
}

// newWorktreeInheritingRedirectForTest builds a git worktree with no .beads of
// its own, whose main checkout's .beads redirects to a separate storage repo.
// The worktree sits outside the main checkout, so discovery reaches that
// redirect only through GetWorktreeFallbackBeadsDir, never by walking up.
func newWorktreeInheritingRedirectForTest(t *testing.T) (cwd, repoRoot, redirectBeadsDir, storageBeadsDir string) {
	t.Helper()

	mainCheckout := filepath.Join(t.TempDir(), "main")
	initGitRepoForContextTest(t, mainCheckout)
	runGitForWorktreeTest(t, mainCheckout, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, mainCheckout, "config", "user.name", "test")
	runGitForWorktreeTest(t, mainCheckout, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitForWorktreeTest(t, mainCheckout, "worktree", "add", "-q", "--detach", worktree)

	storageBeadsDir = newStorageBeadsDirForTest(t)
	redirectBeadsDir = filepath.Join(mainCheckout, ".beads")
	writeRedirectForTest(t, redirectBeadsDir, storageBeadsDir)
	return worktree, worktree, redirectBeadsDir, storageBeadsDir
}

// setChangeDirForTest simulates `bd -C dir` for role detection, which reads the
// changeDir package var directly rather than the (already rebound) BEADS_DIR.
func setChangeDirForTest(t *testing.T, dir string) {
	t.Helper()
	old := changeDir
	changeDir = dir
	t.Cleanup(func() { changeDir = old })
}

// assertActiveRepoPath compares through EvalSymlinks: git resolves the physical
// path while t.TempDir may hand back a symlinked one.
func assertActiveRepoPath(t *testing.T, got, want, why string) {
	t.Helper()
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve got %q: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("resolve want %q: %v", want, err)
	}
	if gotResolved != wantResolved {
		t.Fatalf("activeRepoPathForRouting() = %q (resolved %q), want %q (resolved %q) — %s",
			got, gotResolved, want, wantResolved, why)
	}
}

func TestActiveRepoPathForRoutingKeepsWorkspaceWhenExplicitBeadsDirNamesWorkspaceRedirect(t *testing.T) {
	resetRepoContextCachesForTest(t)

	workspace, _ := newRedirectedWorkspaceForTest(t)

	t.Chdir(workspace)
	t.Setenv("BEADS_DIR", filepath.Join(workspace, ".beads"))
	setBeadsDirStartupProvenanceForTest(t, true)

	assertActiveRepoPath(t, activeRepoPathForRouting(), workspace,
		"exporting the workspace's OWN .beads selects the workspace, not the storage root its redirect points at")
}

func TestActiveRepoPathForRoutingKeepsWorkspaceWhenExplicitBeadsDirNamesRedirectTarget(t *testing.T) {
	resetRepoContextCachesForTest(t)

	workspace, storageBeadsDir := newRedirectedWorkspaceForTest(t)

	t.Chdir(workspace)
	// The bd-wayc3 shape: tooling pre-sets BEADS_DIR to the already-resolved
	// redirect target. That names where the DATA lives, not a different
	// project, so it must not move role detection off the workspace. Both
	// spellings reach here identically because ExplicitBeadsDir follows the
	// redirect.
	t.Setenv("BEADS_DIR", storageBeadsDir)
	setBeadsDirStartupProvenanceForTest(t, true)

	assertActiveRepoPath(t, activeRepoPathForRouting(), workspace,
		"a BEADS_DIR naming the CWD workspace's own redirect target is a vacuous selection")
}

func TestActiveRepoPathForRoutingBindsChangeDirWorkspaceAcrossRedirect(t *testing.T) {
	resetRepoContextCachesForTest(t)

	workspace, storageBeadsDir := newRedirectedWorkspaceForTest(t)
	callerDir := filepath.Join(t.TempDir(), "caller")
	initGitRepoForContextTest(t, callerDir)

	// `bd -C <workspace>` from an unrelated CWD, which is the GH#4242 shape:
	// applyChangeDirSelection has already rewritten BEADS_DIR to the resolved
	// target, so only the -C argument still names the project the user chose.
	// A CWD-anchored redirect probe cannot see this case at all.
	t.Chdir(callerDir)
	t.Setenv("BEADS_DIR", storageBeadsDir)
	setBeadsDirStartupProvenanceForTest(t, false)
	setChangeDirForTest(t, workspace)

	assertActiveRepoPath(t, activeRepoPathForRouting(), workspace,
		"bd -C <redirected-workspace> must bind beads.role to the selected workspace, not to its storage repo")
}

func TestActiveRepoPathForRoutingHonorsChangeDirSelectionWithoutRedirect(t *testing.T) {
	resetRepoContextCachesForTest(t)

	targetDir := filepath.Join(t.TempDir(), "target")
	initGitRepoForContextTest(t, targetDir)
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")

	callerDir := filepath.Join(t.TempDir(), "caller")
	initGitRepoForContextTest(t, callerDir)

	// Control for the three tests above: with no redirect in play, an explicit
	// -C selection must still move role detection to the selected repo. That is
	// the GH#4242 fix itself, and the redirect handling must not undo it.
	t.Chdir(callerDir)
	t.Setenv("BEADS_DIR", targetBeadsDir)
	setBeadsDirStartupProvenanceForTest(t, false)
	setChangeDirForTest(t, targetDir)

	assertActiveRepoPath(t, activeRepoPathForRouting(), targetDir,
		"an explicit selection of a non-redirected project must still win")
}

// TestActiveRepoPathForRoutingKeepsWorkspaceWhenExplicitBeadsDirNamesNonRootRedirect
// carries the two exported-BEADS_DIR workspace tests past the repo root: a
// workspace nested below its repo root, and a git worktree with no .beads of its
// own that inherits the main checkout's. Naming the redirect discovery finds
// there is just as vacuous, so every state of the variable must answer what the
// command answers with BEADS_DIR unset. The rebind state is what a live command
// sees, and there BEADS_DIR no longer names the redirect at all.
func TestActiveRepoPathForRoutingKeepsWorkspaceWhenExplicitBeadsDirNamesNonRootRedirect(t *testing.T) {
	for _, shape := range []struct {
		name  string
		build func(t *testing.T) (cwd, repoRoot, redirectBeadsDir, storageBeadsDir string)
	}{
		{name: "nested workspace", build: newNestedRedirectedWorkspaceForTest},
		{name: "worktree without its own .beads", build: newWorktreeInheritingRedirectForTest},
	} {
		t.Run(shape.name, func(t *testing.T) {
			cwd, repoRoot, redirectBeadsDir, storageBeadsDir := shape.build(t)
			t.Chdir(cwd)
			setChangeDirForTest(t, "")
			for _, state := range []struct {
				name, beadsDir string
				exported       bool
			}{
				{name: "unset", beadsDir: "", exported: false},
				{name: "as exported", beadsDir: redirectBeadsDir, exported: true},
				{name: "after the pre-RunE rebind", beadsDir: storageBeadsDir, exported: true},
			} {
				t.Run("BEADS_DIR "+state.name, func(t *testing.T) {
					resetRepoContextCachesForTest(t)
					t.Setenv("BEADS_DIR", state.beadsDir)
					setBeadsDirStartupProvenanceForTest(t, state.exported)
					assertActiveRepoPath(t, activeRepoPathForRouting(), repoRoot,
						"naming the CWD workspace's own redirect must not move role detection to the storage root")
				})
			}
		})
	}
}

func TestActiveRepoPathForRoutingHonorsExplicitBeadsDirFromRedirectedWorkspace(t *testing.T) {
	resetRepoContextCachesForTest(t)

	workspace, _ := newRedirectedWorkspaceForTest(t)
	targetDir := filepath.Join(t.TempDir(), "target")
	initGitRepoForContextTest(t, targetDir)
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	writeTestConfigYAML(t, targetBeadsDir, "")

	// The converse of the vacuous cases: from a redirected workspace, an
	// exported BEADS_DIR naming a DIFFERENT project is a real selection, and
	// the CWD workspace's own redirect must not claim it. Only the redirect
	// target comparison tells the two apart.
	t.Chdir(workspace)
	t.Setenv("BEADS_DIR", targetBeadsDir)
	setBeadsDirStartupProvenanceForTest(t, true)
	setChangeDirForTest(t, "")

	assertActiveRepoPath(t, activeRepoPathForRouting(), targetDir,
		"a BEADS_DIR naming another project is explicit selection, even from a redirected workspace")
}

func TestActiveRepoPathForRoutingBindsForeignRedirectedBeadsDirToStorage(t *testing.T) {
	workspace, storageBeadsDir := newRedirectedWorkspaceForTest(t)
	callerDir := filepath.Join(t.TempDir(), "caller")
	initGitRepoForContextTest(t, callerDir)

	// The exported BEADS_DIR names a redirected workspace that is NOT the CWD
	// repo, and the CWD repo has no .beads, so GetRedirectInfo would fall back
	// to BEADS_DIR itself. The selection must not vouch for itself as the CWD
	// workspace's own redirect and bind the unrelated caller's role. Only the
	// exported arm pins that. The rebound storage .beads holds no redirect to
	// vouch with, so the rebind arm (all a live command ever sees) checks only
	// that both states of the variable agree.
	t.Chdir(callerDir)
	setBeadsDirStartupProvenanceForTest(t, true)
	setChangeDirForTest(t, "")
	for _, state := range []struct{ name, beadsDir string }{
		{name: "as exported", beadsDir: filepath.Join(workspace, ".beads")},
		{name: "after the pre-RunE rebind", beadsDir: storageBeadsDir},
	} {
		resetRepoContextCachesForTest(t)
		t.Setenv("BEADS_DIR", state.beadsDir)
		assertActiveRepoPath(t, activeRepoPathForRouting(), filepath.Dir(storageBeadsDir),
			"BEADS_DIR "+state.name+": a foreign redirected selection binds its storage root, not the CWD repo")
	}
}

// resetRolePathWarningForTest re-arms the once-per-process non-repo warning so
// a test asserting on it does not depend on whether an earlier test in the
// binary already spent it.
func resetRolePathWarningForTest(t *testing.T) {
	t.Helper()
	old := rolePathNotARepoWarned
	rolePathNotARepoWarned = false
	t.Cleanup(func() { rolePathNotARepoWarned = old })
}

func TestDetectUserRoleForActiveRepoWarnsWhenRolePathIsNotARepo(t *testing.T) {
	resetRepoContextCachesForTest(t)
	resetRolePathWarningForTest(t)

	// A storage dir outside any git repo: role detection lands on its parent,
	// where `git config --get beads.role` still succeeds by reading the user's
	// GLOBAL config, so routing.DetectUserRole returns before its own
	// "beads.role not configured" notice can fire. The path must not be silent.
	storageDir := t.TempDir()
	storageBeadsDir := filepath.Join(storageDir, ".beads")
	writeTestConfigYAML(t, storageBeadsDir, "")

	t.Chdir(t.TempDir())
	t.Setenv("BEADS_DIR", storageBeadsDir)
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

	// Detect twice: an ID lookup re-runs role detection once per missing ID,
	// and the warning must not repeat with it.
	for range 2 {
		if _, err := detectUserRoleForActiveRepo(); err != nil {
			t.Fatalf("detectUserRoleForActiveRepo: %v", err)
		}
	}
	_ = writeStderr.Close()
	stderrOutput, readErr := io.ReadAll(readStderr)
	if readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	if n := strings.Count(string(stderrOutput), "is not a git repository"); n != 1 {
		t.Fatalf("non-repo role path warned %d times in one process, want exactly once; stderr:\n%s", n, stderrOutput)
	}
}

// unsetRoutingConfigEnvForTest removes every env spelling of the routing keys.
// Blanking them is not enough: GetValueSource counts a set-but-empty variable
// as an env source, viper then answers with the key's default, and the
// defaults for routing.maintainer and routing.contributor are non-empty, so a
// blanked variable would configure a role repo the test never set.
func unsetRoutingConfigEnvForTest(t *testing.T) {
	t.Helper()
	for _, key := range routingConfigKeys {
		suffix := strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(key))
		for _, name := range []string{"BD_" + suffix, "BEADS_" + suffix} {
			t.Setenv(name, "") // registers the restore
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
		}
	}
}

// TestDetermineAutoRoutedRepoPathDetectsRoleOnlyWhenRoutingReadsIt pins role
// detection to the fully resolved routing config. The fixture makes any
// detection visible: the project is not a git repository, so its role path
// warns, while a global beads.role gives `git config --get` something to fall
// back on. Routing that cannot read the role must leave stderr clean, and the
// legacy contributor.auto_route switch must still detect with routing.mode
// empty.
func TestDetermineAutoRoutedRepoPathDetectsRoleOnlyWhenRoutingReadsIt(t *testing.T) {
	planningDir := t.TempDir()
	tests := []struct {
		name       string
		config     map[string]string
		wantRepo   string
		wantDetect bool
	}{
		{name: "routing unset", wantRepo: "."},
		{
			name:     "explicit mode",
			config:   map[string]string{"routing.mode": "explicit", "routing.contributor": planningDir},
			wantRepo: ".",
		},
		{
			name:     "auto mode without a role repo",
			config:   map[string]string{"routing.mode": "auto"},
			wantRepo: ".",
		},
		{
			name:       "auto mode",
			config:     map[string]string{"routing.mode": "auto", "routing.contributor": planningDir},
			wantRepo:   planningDir,
			wantDetect: true,
		},
		{
			name:       "legacy contributor.auto_route",
			config:     map[string]string{"contributor.auto_route": "true", "contributor.planning_repo": planningDir},
			wantRepo:   planningDir,
			wantDetect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"} {
				t.Setenv(key, home)
			}
			if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[beads]\n\trole = contributor\n"), 0o600); err != nil {
				t.Fatalf("write global gitconfig: %v", err)
			}
			unsetRoutingConfigEnvForTest(t)

			project := t.TempDir()
			writeTestConfigYAML(t, filepath.Join(project, ".beads"), "")
			t.Chdir(project)
			t.Setenv("BEADS_DIR", "")
			setBeadsDirStartupProvenanceForTest(t, false)
			setChangeDirForTest(t, "")

			initConfigForTest(t)
			for key, value := range tt.config {
				config.Set(key, value)
			}
			resetRepoContextCachesForTest(t)
			resetRolePathWarningForTest(t)

			var got string
			stderr := captureStderr(t, func() {
				got, _ = determineAutoRoutedRepoPath(context.Background(), nil)
			})
			if got != tt.wantRepo {
				t.Errorf("determineAutoRoutedRepoPath() = %q, want %q", got, tt.wantRepo)
			}
			if detected := strings.Contains(stderr, "is not a git repository"); detected != tt.wantDetect {
				t.Errorf("role detected = %v, want %v; stderr:\n%s", detected, tt.wantDetect, stderr)
			}
		})
	}
}

func TestPrepareSelectedCommandContext_DoesNotMergeCallerConfigForUnsetKeys(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	clearActorEnv(t)

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
