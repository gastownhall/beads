package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/stretchr/testify/require"
)

func TestGuardHookWritePathIgnoresInheritedGitRouting(t *testing.T) {
	runGit := func(repo string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = repo
		cmd.Env = gitenv.ScrubRouting(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	target, decoy := t.TempDir(), t.TempDir()
	for _, repo := range []string{target, decoy} {
		runGit(repo, "init", "--quiet")
		runGit(repo, "config", "core.hooksPath", ".git/hooks")
	}
	hooksDir := filepath.Join(target, "hooks")
	if err := os.Mkdir(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(hooksDir, "pre-commit")
	owned := filepath.Join(hooksDir, "pre-push")
	untracked := filepath.Join(hooksDir, "post-merge")
	contents := map[string]string{
		foreign:                            "#!/bin/sh\necho team hook\n",
		owned:                              "#!/bin/sh\n" + generateHookSection("pre-push"),
		untracked:                          "#!/bin/sh\necho untracked hook\n",
		filepath.Join(decoy, "decoy-only"): "decoy\n",
	}
	for path, content := range contents {
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	runGit(target, "add", "--force", "--", "hooks/pre-commit", "hooks/pre-push")
	runGit(decoy, "add", "--force", "--", "decoy-only")
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"both", map[string]string{"GIT_DIR": filepath.Join(target, ".git"), "GIT_WORK_TREE": target}},
		{"repository", map[string]string{"GIT_DIR": filepath.Join(decoy, ".git")}},
		{"worktree", map[string]string{"GIT_DIR": filepath.Join(decoy, ".git"), "GIT_WORK_TREE": decoy}},
		{"index", map[string]string{"GIT_INDEX_FILE": filepath.Join(decoy, ".git", "index")}},
		{"inline_config", map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": decoy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if tc.name == "both" {
				probe := gitCommand("-C", hooksDir, "ls-files", "--error-unmatch", "--", "pre-commit")
				out, err := probe.CombinedOutput()
				require.NoError(t, err, "inherited index must also prove tracking: %s", out)
			}
			if !isGitTrackedFile(foreign) {
				t.Error("inherited routing hid the tracked hook")
			}
			require.Equal(t, "containing repository index", gitTrackedFileContext(foreign), "clean proof takes precedence, including when both views track it")
			require.ErrorContains(t, guardHookWritePath(foreign, false), "tracked by git (containing repository index)")
			if err := guardHookWritePath(foreign, false); err == nil || !strings.Contains(err.Error(), "tracked by git") {
				t.Errorf("expected tracked-file refusal, got %v", err)
			}
			if err := guardHookWritePath(foreign, true); err != nil {
				t.Errorf("shared tracked hook refused: %v", err)
			}
			if !isGitTrackedFile(owned) {
				t.Error("owned hook fixture must remain tracked")
			}
			if err := guardHookWritePath(owned, false); err != nil {
				t.Errorf("bd-owned tracked hook refused: %v", err)
			}
			if isGitTrackedFile(untracked) {
				t.Error("untracked hook reported as tracked")
			}
			require.Empty(t, gitTrackedFileContext(untracked))
			if err := guardHookWritePath(untracked, false); err != nil {
				t.Errorf("untracked hook refused: %v", err)
			}
			for path, want := range contents {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Errorf("guard changed %s: content=%q, err=%v", path, got, err)
				}
			}
		})
	}
}

// setupGuardTestRepo creates a git repo with one tracked script and chdirs
// into it. Returns the repo dir.
func setupGuardTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()

	run := func(args ...string) {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	if err := os.MkdirAll(filepath.Join(repoDir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "scripts", "team-pre-push"), []byte("#!/bin/sh\necho team hook\n"), 0755); err != nil {
		t.Fatal(err)
	}
	run("add", "scripts/team-pre-push")
	run("commit", "-m", "add team hook script")

	t.Chdir(repoDir)
	git.ResetCaches()
	return repoDir
}

// bd-5vdt8: a symlinked hook file must refuse installation instead of
// writing through the link into the target (historically a tracked repo
// script, re-dirtying every clone).
func TestInstallHooksRefusesSymlinkedHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	target := filepath.Join(repoDir, "scripts", "team-pre-push")
	originalContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, hookPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	installErr := installHooksWithOptions(managedHookNames, false, false, false, false)
	if installErr == nil {
		t.Fatal("expected install to refuse symlinked hook, got nil error")
	}
	if !strings.Contains(installErr.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got: %v", installErr)
	}

	afterContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterContent) != string(originalContent) {
		t.Fatalf("symlink target was modified:\n%s", afterContent)
	}

	// Preflight must refuse before writing ANY hook — no partial install.
	for _, name := range managedHookNames {
		if name == "pre-push" {
			continue
		}
		if _, statErr := os.Lstat(filepath.Join(repoDir, ".git", "hooks", name)); !os.IsNotExist(statErr) {
			t.Errorf("hook %s was written despite refusal", name)
		}
	}
}

// bd-5vdt8: a hook file tracked by git (e.g. core.hooksPath pointing into
// the working tree) must refuse installation instead of dirtying the tree.
func TestInstallHooksRefusesTrackedHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	if err := os.MkdirAll(filepath.Join(repoDir, "hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	trackedHook := filepath.Join(repoDir, "hooks", "pre-commit")
	if err := os.WriteFile(trackedHook, []byte("#!/bin/sh\necho tracked hook\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "hooks/pre-commit"},
		{"commit", "-m", "add tracked hook"},
		{"config", "core.hooksPath", "hooks"},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	git.ResetCaches()

	installErr := installHooksWithOptions(managedHookNames, false, false, false, false)
	if installErr == nil {
		t.Fatal("expected install to refuse tracked hook, got nil error")
	}
	if !strings.Contains(installErr.Error(), "tracked by git") {
		t.Fatalf("expected tracked-file refusal, got: %v", installErr)
	}
}

// A tracked hook that bd OWNS (section markers) must still be maintainable:
// teams commit .beads/hooks/ like shared .beads-hooks/, and refusing would
// break reinstall/upgrade for them (caught by TestEmbeddedHooks in CI).
func TestInstallHooksAllowsTrackedBdOwnedHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	hooksDir := filepath.Join(repoDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	bdHook := "#!/usr/bin/env sh\n" + generateHookSection("pre-commit")
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(bdHook), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "hooks/pre-commit"},
		{"commit", "-m", "commit bd-managed hook"},
		{"config", "core.hooksPath", "hooks"},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	git.ResetCaches()

	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("reinstall over a tracked bd-owned hook must succeed, got: %v", err)
	}
}

// bd-5vdt8: injecting the bd section into a hook bd does not own must
// preserve the original as a .backup sidecar.
func TestInstallHooksBacksUpForeignHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	userContent := "#!/bin/sh\necho my custom hook\n"
	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(userContent), 0755); err != nil {
		t.Fatal(err)
	}

	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	backup, err := os.ReadFile(hookPath + ".backup")
	if err != nil {
		t.Fatalf("expected .backup sidecar: %v", err)
	}
	if string(backup) != userContent {
		t.Fatalf(".backup does not match original content:\n%s", backup)
	}

	merged, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "echo my custom hook") {
		t.Fatal("user content lost from hook after injection")
	}
	if !strings.Contains(string(merged), hookSectionBeginPrefix) {
		t.Fatal("bd section not injected into hook")
	}

	// Reinstall must not overwrite the backup with the merged content.
	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("reinstall failed: %v", err)
	}
	backup2, err := os.ReadFile(hookPath + ".backup")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup2) != userContent {
		t.Fatal(".backup was clobbered on reinstall")
	}
}

// externalIntegrationHook is the shape beads prescribes for external hook
// managers (GH#946): the manager owns the file, and one line delegates to bd.
// It carries no beads marker, so bd must recognize it as a working integration
// (bd hooks list says installed) while still treating the file as foreign.
//
// The write-path tests below exist because recognizing it via IsBdHook — the
// ownership predicate — made bd destroy exactly this file. They all fail if the
// classification is carried on IsBdHook instead of IsExternalIntegration.
const externalIntegrationHook = "#!/bin/sh\nlefthook run pre-commit \"$@\"\nbd hooks run pre-commit \"$@\"\n"

// A hook bd does not own must be backed up before injection even when it
// delegates to bd: the delegation makes it a beads integration, not beads'
// property.
func TestInstallHooksBacksUpExternalIntegrationHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(externalIntegrationHook), 0755); err != nil {
		t.Fatal(err)
	}

	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	backup, err := os.ReadFile(hookPath + ".backup")
	if err != nil {
		t.Fatalf("expected .backup sidecar for an external-manager hook: %v", err)
	}
	if string(backup) != externalIntegrationHook {
		t.Fatalf(".backup does not match original content:\n%s", backup)
	}

	merged, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "lefthook run pre-commit") {
		t.Fatal("external hook manager's own line lost from hook after injection")
	}
	if !strings.Contains(string(merged), hookSectionBeginPrefix) {
		t.Fatal("bd section not injected into hook")
	}
}

// The tracked-file refusal must still fire for a delegating hook: a committed
// team hook under core.hooksPath that calls bd hooks run is the shape this
// repository itself ships in .githooks/pre-commit.
func TestInstallHooksRefusesTrackedExternalIntegrationHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	if err := os.MkdirAll(filepath.Join(repoDir, "hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	trackedHook := filepath.Join(repoDir, "hooks", "pre-commit")
	if err := os.WriteFile(trackedHook, []byte(externalIntegrationHook), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "hooks/pre-commit"},
		{"commit", "-m", "add tracked external integration hook"},
		{"config", "core.hooksPath", "hooks"},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	git.ResetCaches()

	installErr := installHooksWithOptions(managedHookNames, false, false, false, false)
	if installErr == nil {
		t.Fatal("expected install to refuse a tracked external-manager hook, got nil error")
	}
	if !strings.Contains(installErr.Error(), "tracked by git") {
		t.Fatalf("expected tracked-file refusal, got: %v", installErr)
	}
}

// Uninstall removes files bd owns. A delegating hook bd never wrote must
// survive: deleting it silently destroys the external manager's hook, with no
// .backup to restore from.
func TestUninstallHooksPreservesExternalIntegrationHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(externalIntegrationHook), 0755); err != nil {
		t.Fatal(err)
	}

	if err := uninstallHooks(); err != nil {
		t.Fatalf("uninstallHooks() failed: %v", err)
	}

	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("external-manager hook must survive uninstall: %v", err)
	}
	if string(content) != externalIntegrationHook {
		t.Fatalf("external-manager hook was modified by uninstall:\n%s", content)
	}
}

// bd hooks run chains to <hook>.old, so a .old that itself delegates via
// "bd hooks run" would re-enter bd and run again without bound. The recursion
// guard must skip it even though it is not IsBdHook, and must still run a plain
// .old.
//
// The delegating fixture comments its "bd hooks run" line out. The classifier
// matches the call anywhere in the file, so it still classifies as an external
// integration, but if the guard stops firing it only leaves the marker behind
// instead of recursing.
func TestRunChainedHookSkipsDelegatingOldHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chained .old fixtures are POSIX shell scripts")
	}
	for _, tc := range []struct {
		name      string
		delegates bool
		wantRun   bool
	}{
		{name: "delegating .old is skipped", delegates: true},
		{name: "plain .old runs", wantRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := setupGuardTestRepo(t)

			hooksDir := filepath.Join(repoDir, ".git", "hooks")
			if err := os.MkdirAll(hooksDir, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "chained-hook-ran")
			script := "#!/bin/sh\n"
			if tc.delegates {
				script += "# bd hooks run pre-commit \"$@\"\n"
			}
			script += "touch '" + marker + "'\n"
			oldHook := filepath.Join(hooksDir, "pre-commit.old")
			if err := os.WriteFile(oldHook, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}

			// Pin the classification the guard keys on, so a fixture that
			// stopped looking like an integration cannot pass for the wrong
			// reason.
			info, err := getHookVersion(oldHook)
			if err != nil {
				t.Fatal(err)
			}
			if info.IsBdHook || info.IsExternalIntegration != tc.delegates {
				t.Fatalf("fixture classified as %+v, want IsExternalIntegration=%v and IsBdHook=false", info, tc.delegates)
			}

			if code := runChainedHook("pre-commit", nil); code != 0 {
				t.Fatalf("runChainedHook() = %d, want 0", code)
			}
			_, statErr := os.Stat(marker)
			if ran := statErr == nil; ran != tc.wantRun {
				t.Fatalf("chained .old ran = %v, want %v", ran, tc.wantRun)
			}
		})
	}
}

// Shared installs (.beads-hooks/) are deliberately committed, so the
// tracked-file guard must not fire for them.
func TestGuardHookWritePathAllowsTrackedWhenShared(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	tracked := filepath.Join(repoDir, "scripts", "team-pre-push")
	if err := guardHookWritePath(tracked, true); err != nil {
		t.Fatalf("expected tracked file to be allowed with allowTracked=true, got: %v", err)
	}
	if err := guardHookWritePath(tracked, false); err == nil {
		t.Fatal("expected tracked file to be refused with allowTracked=false")
	}
}

// The hook-migration apply path shares the same guard: a symlinked hook
// must refuse the migrated write.
func TestApplyHookMigrationRefusesSymlink(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	target := filepath.Join(repoDir, "scripts", "team-pre-push")
	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.Symlink(target, hookPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	plan := hookMigrationExecutionPlan{
		WriteOps: []hookMigrationWriteOp{
			{
				HookName:   "pre-commit",
				HookPath:   hookPath,
				State:      "missing_no_artifacts",
				SourceKind: hookMigrationWriteFromTemplate,
			},
		},
	}
	if _, err := applyHookMigrationExecution(plan); err == nil {
		t.Fatal("expected migration apply to refuse symlinked hook")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got: %v", err)
	}
}

func TestGuardHookWritePathHonorsInheritedRepository(t *testing.T) {
	// Isolate configuration and restore every inherited routing entry afterwards.
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, os.Getenv(key))
		}
	}
	if _, err := gitenv.ClearRouting(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "nondefault-config"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "0")
	for _, name := range []string{"bare_worktree", "global_safe_directory"} {
		t.Run(name, func(t *testing.T) {
			repo, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			runGit := func(args ...string) {
				t.Helper()
				cmd := gitCommand(args...)
				cmd.Dir = repo
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			if name == "bare_worktree" {
				gitDir := t.TempDir()
				runGit("init", "--bare", "--quiet", gitDir)
				t.Setenv("GIT_DIR", gitDir)
				t.Setenv("GIT_WORK_TREE", repo)
			} else {
				runGit("init", "--quiet")
			}
			hooksDir := filepath.Join(repo, ".githooks")
			if err := os.Mkdir(hooksDir, 0755); err != nil {
				t.Fatal(err)
			}
			hook := filepath.Join(hooksDir, "pre-commit")
			const content = "#!/bin/sh\necho team hook\n"
			if err := os.WriteFile(hook, []byte(content), 0755); err != nil {
				t.Fatal(err)
			}
			runGit("config", "core.hooksPath", ".githooks")
			runGit("add", "--force", "--", ".githooks/pre-commit")
			if name == "global_safe_directory" {
				// Reproduce a lower-priority ambient allowance even on an isolated host.
				ambient := filepath.Join(home, ".config", "git")
				if err := os.MkdirAll(ambient, 0755); err != nil {
					t.Fatal(err)
				}
				runGit("config", "--file", filepath.Join(ambient, "config"), "safe.directory", "*")
				// This default-global reset survives ScrubRouting removing GIT_CONFIG_*.
				runGit("config", "--file", filepath.Join(home, ".gitconfig"), "safe.directory", "")
				runGit("config", "--global", "--add", "safe.directory", filepath.ToSlash(repo))
				// Exercise Git's ownership-check control flow, not OS ownership/ACLs.
				t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
			}
			t.Chdir(repo)
			git.ResetCaches()
			t.Cleanup(git.ResetCaches)
			resolved, err := git.GetGitHooksDir()
			if err != nil {
				t.Fatal(err)
			}
			gotInfo, gotErr := os.Stat(resolved)
			wantInfo, wantErr := os.Stat(hooksDir)
			if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
				t.Fatalf("resolved hooks %q must identify %q: %v, %v", resolved, hooksDir, gotErr, wantErr)
			}
			inherited := os.Environ()
			args := []string{"-C", hooksDir, "ls-files", "--error-unmatch", "--", "pre-commit"}
			clean := gitCommand(args...)
			clean.Env = gitenv.ScrubRouting(inherited)
			out, cleanErr := clean.CombinedOutput()
			if cleanErr == nil {
				t.Fatalf("fixture must require inherited context, scrubbed probe succeeded: %s", out)
			}
			// The inherited fallback runs only when the scrubbed probe fails
			// for a configuration reason. Exit 1 means "repository reached,
			// path is not tracked" and is final, so a fixture that produced it
			// would make the fallback unreachable and this test vacuous.
			if exit, ok := cleanErr.(*exec.ExitError); ok && exit.ExitCode() == 1 {
				t.Fatalf("fixture must fail the scrubbed probe for a configuration reason, got exit 1: %s", out)
			}
			fallback := gitCommand(args...)
			fallback.Env = inherited
			if out, err := fallback.CombinedOutput(); err != nil {
				t.Fatalf("inherited tracking precondition: %v\n%s", err, out)
			}
			for _, check := range []struct {
				name string
				run  func() error
			}{
				{"guard", func() error { return guardHookWritePath(hook, false) }},
				{"install", func() error { return installHooksWithOptions(managedHookNames, false, false, false, false) }},
				{"migrate", func() error {
					_, err := applyHookMigrationExecution(hookMigrationExecutionPlan{
						WriteOps: []hookMigrationWriteOp{{HookName: "pre-commit", HookPath: hook, SourceKind: hookMigrationWriteFromTemplate}},
					})
					return err
				}},
			} {
				if err := check.run(); err == nil || !strings.Contains(err.Error(), "tracked by git") {
					t.Errorf("%s must refuse tracked foreign hook, got %v", check.name, err)
				}
				if got, err := os.ReadFile(hook); err != nil || string(got) != content {
					t.Errorf("%s changed hook: %q, %v", check.name, got, err)
				}
				entries, err := os.ReadDir(hooksDir)
				if err != nil || len(entries) != 1 || entries[0].Name() != "pre-commit" {
					t.Errorf("%s created backup or other hook: %v, %v", check.name, entries, err)
				}
			}
		})
	}
}

func TestGuardHookWritePathAllowsFileWhenGitUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-commit")
	if err := os.WriteFile(path, []byte("foreign hook\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	if _, err := exec.LookPath("git"); err == nil {
		t.Fatal("fixture must make Git unavailable")
	}
	if err := guardHookWritePath(path, false); err != nil {
		t.Fatalf("both tracking errors must retain the untracked policy: %v", err)
	}
}

func TestInitHooksContextGuardPreservesOwnership(t *testing.T) {
	for _, name := range []string{"tracked", "bd_owned", "symlink", "captured_fallback"} {
		t.Run(name, func(t *testing.T) {
			selected, _, _, _ := newInitHooksFixture(t)
			storage := filepath.Join(selected, "local-storage")
			hooksDir := filepath.Join(storage, "hooks")
			if name == "captured_fallback" {
				storage = t.TempDir()
				hooksDir = filepath.Join(storage, "hooks")
			}
			require.NoError(t, os.MkdirAll(hooksDir, 0755))
			hook := filepath.Join(hooksDir, "pre-commit")
			content := "#!/bin/sh\necho foreign\n"
			if name == "bd_owned" {
				content = "#!/bin/sh\n" + generateHookSection("pre-commit")
			}
			if name == "symlink" {
				target := filepath.Join(t.TempDir(), "foreign hook")
				require.NoError(t, os.WriteFile(target, []byte(content), 0755))
				if err := os.Symlink(target, hook); err != nil {
					t.Skipf("symlink capability unavailable: %v", err)
				}
			} else {
				require.NoError(t, os.WriteFile(hook, []byte(content), 0755))
				if name == "captured_fallback" {
					bare := t.TempDir()
					initExcludeGit(t, bare, "init", "--bare", "--quiet")
					initExcludeGit(t, storage, "--git-dir", bare, "--work-tree", storage, "add", "hooks/pre-commit")
					t.Setenv("GIT_DIR", bare)
					t.Setenv("GIT_WORK_TREE", storage)
					t.Setenv("GIT_INDEX_FILE", filepath.Join(bare, "index"))
				} else {
					initExcludeGit(t, selected, "add", "--force", "--", hook)
				}
			}
			fs, hooks, err := withInitHooks(nil, selected, storage)
			require.NoError(t, err)
			if name == "captured_fallback" {
				require.False(t, isGitTrackedFileWithEnv(hook, hooks.env, hooks.env), "clean view must not supply the proof")
				require.True(t, isGitTrackedFileWithEnv(hook, hooks.env, hooks.inheritedEnv), "inherited view must prove tracking")
			}
			t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
			err = fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, BeadsHooks: true})
			if name == "bd_owned" {
				require.NoError(t, err)
				require.Contains(t, string(readInitHooksFile(t, hook)), hookSectionBeginPrefix)
			} else {
				want := "tracked by git"
				if name == "symlink" {
					want = "symlink"
				}
				require.ErrorContains(t, err, want)
				if name == "captured_fallback" {
					require.ErrorContains(t, err, "tracked by git (inherited Git index)")
					// The remediation follows the proof: `git rm --cached` from the
					// operator's shell aims at whichever index that shell routes to,
					// so the inherited case has to name clearing the routing instead.
					require.ErrorContains(t, err, "Clear the inherited Git routing environment and re-run")
					for _, entry := range hooks.inheritedEnv {
						if strings.HasPrefix(entry, "GIT_DIR=") {
							require.NotContains(t, err.Error(), strings.TrimPrefix(entry, "GIT_DIR="))
						}
					}
				} else if name == "tracked" {
					require.ErrorContains(t, err, "tracked by git (containing repository index)")
					require.ErrorContains(t, err, "Untrack it (git rm --cached) or move hooks to an untracked directory")
					require.NotContains(t, err.Error(), "Clear the inherited Git routing environment")
				}
				require.Equal(t, content, string(readInitHooksFile(t, hook)))
				for _, other := range []string{"post-merge", "pre-commit.backup"} {
					_, err := os.Lstat(filepath.Join(hooksDir, other))
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			}
		})
	}
}

// GH#6348: bd appends a POSIX-shell integration block to a foreign pre-push
// hook whose shebang is python3, which leaves the hook a Python SyntaxError.
// bd must never splice shell code into a hook interpreted by a non-shell
// interpreter.
func TestInstallHooksDoesNotAppendShellToPythonHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	userContent := "#!/usr/bin/env python3\nimport sys\n\n\ndef main():\n    return 0\n\n\nif __name__ == \"__main__\":\n    sys.exit(main())\n"
	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(userContent), 0755); err != nil {
		t.Fatal(err)
	}

	_ = installHooksWithOptions(managedHookNames, false, false, false, false)

	got, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(got), "#!/usr/bin/env python3") &&
		strings.Contains(string(got), "if command -v bd >/dev/null 2>&1; then") {
		t.Fatalf("bd appended its shell section to a python3 hook (Python SyntaxError at runtime):\n%s", got)
	}
}

// nonShellPythonPrePush is a pre-commit/prek style Python pre-push hook that
// ends in sys.exit, the shape GH#6348 reported.
const nonShellPythonPrePush = "#!/usr/bin/env python3\nimport sys\n\n\ndef main():\n    return 0\n\n\nif __name__ == \"__main__\":\n    sys.exit(main())\n"

func writeNonShellHookForTest(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
}

func readNonShellHookForTest(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(got)
}

// installHooksCapturingStderr runs a default-mode install of every managed
// hook and returns what it printed on stderr.
func installHooksCapturingStderr(t *testing.T, beadsHooks bool) string {
	t.Helper()
	var err error
	stderr := captureStderr(t, func() {
		err = installHooksWithOptions(managedHookNames, false, false, false, beadsHooks)
	})
	require.NoError(t, err, "stderr:\n%s", stderr)
	return stderr
}

// runHooksInstallCommandForTest runs bd hooks install's RunE and returns its
// stdout and stderr separately.
func runHooksInstallCommandForTest(t *testing.T, asJSON bool) (stdout, stderr string) {
	t.Helper()
	oldJSON := jsonOutput
	jsonOutput = asJSON
	t.Cleanup(func() { jsonOutput = oldJSON })

	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)
	outC, errC := make(chan string, 1), make(chan string, 1)
	drain := func(r *os.File, c chan<- string) {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			b.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		c <- b.String()
	}
	go drain(outR, outC)
	go drain(errR, errC)
	os.Stdout, os.Stderr = outW, errW
	runErr := hooksInstallCmd.RunE(hooksInstallCmd, nil)
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	stdout, stderr = <-outC, <-errC
	require.NoError(t, runErr, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	return stdout, stderr
}

// TestInstallHooks_NonShell pins GH#6348: an install never splices bd's
// POSIX-sh section into a hook run by another interpreter, repairs complete
// sections an earlier install appended, leaves ambiguous markers and binary
// content alone, and keeps the shell path unchanged.
func TestInstallHooks_NonShell(t *testing.T) {
	t.Run("AC1_python_pre_push_is_skipped_byte_identical", func(t *testing.T) {
		repoDir := setupGuardTestRepo(t)
		hooksDir := filepath.Join(repoDir, ".git", "hooks")
		hookPath := filepath.Join(hooksDir, "pre-push")
		writeNonShellHookForTest(t, hookPath, nonShellPythonPrePush)

		stderr := installHooksCapturingStderr(t, false)

		require.Equal(t, nonShellPythonPrePush, readNonShellHookForTest(t, hookPath), "python hook must stay byte-identical")
		_, err := os.Lstat(hookPath + ".backup")
		require.True(t, os.IsNotExist(err), "no pre-push.backup may be created for a skipped hook: %v", err)
		for _, want := range []string{"pre-push", hookPath, "python3", "bd hooks run pre-push", "arguments and stdin"} {
			require.Contains(t, stderr, want, "skip warning must name %q", want)
		}
		require.Contains(t, readNonShellHookForTest(t, filepath.Join(hooksDir, "pre-commit")), hookSectionBeginPrefix,
			"the other hooks are still installed")
	})

	t.Run("AC2_beads_mode_preserves_python_and_configures_hooks_path", func(t *testing.T) {
		_, repo := setupSymlinkedHooksDirRepo(t)
		writeNonShellHookForTest(t, filepath.Join(repo, ".git", "hooks", "pre-push"), nonShellPythonPrePush)

		stderr := installHooksCapturingStderr(t, true)

		target := filepath.Join(repo, ".beads", "hooks")
		require.Equal(t, nonShellPythonPrePush, readNonShellHookForTest(t, filepath.Join(target, "pre-push")),
			"the preserved python pre-push must stay byte-identical")
		require.Contains(t, stderr, "python3")
		for _, name := range managedHookNames {
			if name == "pre-push" {
				continue
			}
			require.Contains(t, readNonShellHookForTest(t, filepath.Join(target, name)), hookSectionBeginPrefix, "%s installed", name)
		}
		out, err := exec.Command("git", "-C", repo, "config", "--get", "core.hooksPath").Output()
		require.NoError(t, err)
		configured := strings.TrimSpace(string(out))
		if !filepath.IsAbs(configured) {
			configured = filepath.Join(repo, configured)
		}
		wantDir, err := filepath.EvalSymlinks(target)
		require.NoError(t, err)
		gotDir, err := filepath.EvalSymlinks(configured)
		require.NoError(t, err)
		require.Equal(t, wantDir, gotDir, "core.hooksPath selects the beads hooks directory")
	})

	t.Run("AC3_human_and_json_report_the_skip", func(t *testing.T) {
		repoDir := setupGuardTestRepo(t)
		writeNonShellHookForTest(t, filepath.Join(repoDir, ".git", "hooks", "pre-push"), nonShellPythonPrePush)

		stdout, stderr := runHooksInstallCommandForTest(t, false)
		installed, rest, ok := strings.Cut(stdout, "Installed hooks:\n")
		require.True(t, ok, "human output lists installed hooks:\n%s", stdout)
		_ = installed
		installedList, skippedPart, _ := strings.Cut(rest, "\n\n")
		require.NotContains(t, installedList, "- pre-push", "pre-push is not an installed hook")
		require.Contains(t, installedList, "- pre-commit")
		require.Contains(t, skippedPart, "Skipped hooks")
		require.Contains(t, skippedPart, "- pre-push")
		require.Contains(t, stderr, "python3")

		stdout, stderr = runHooksInstallCommandForTest(t, true)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(stdout), &result), "stdout must be pure JSON:\n%s", stdout)
		require.Equal(t, true, result["success"])
		require.Equal(t, []any{"pre-push"}, result["skipped_hooks"])
		require.NotContains(t, stdout, "Warning")
		require.Contains(t, stderr, "pre-push")
		require.Contains(t, stderr, "python3")
	})

	t.Run("AC4_repairs_complete_sections_once", func(t *testing.T) {
		section := generateHookSection("pre-push")
		crlf := strings.ReplaceAll(nonShellPythonPrePush, "\n", "\r\n")
		noFinalNL := strings.TrimSuffix(nonShellPythonPrePush, "\n")
		node := "#!/usr/bin/env node\nprocess.exit(0)\n"
		ruby := "#!/usr/bin/ruby\nexit 0\n"
		fish := "#!/usr/bin/env fish\nexit 0\n"
		cases := []struct {
			name, before, want string
		}{
			{"appended_by_injector", injectHookSection(nonShellPythonPrePush, section), nonShellPythonPrePush},
			{"crlf", crlf + "\r\n" + strings.ReplaceAll(section, "\n", "\r\n"), crlf + "\r\n"},
			{"missing_final_newline", injectHookSection(noFinalNL, section), noFinalNL + "\n"},
			{"content_after_end", nonShellPythonPrePush + "\n" + section + "print('after')\n", nonShellPythonPrePush + "print('after')\n"},
			{"two_disjoint_pairs", nonShellPythonPrePush + "\n" + section + "x = 1\n\n" + section, nonShellPythonPrePush + "x = 1\n"},
			{"node", injectHookSection(node, section), node},
			{"ruby", injectHookSection(ruby, section), ruby},
			{"fish", injectHookSection(fish, section), fish},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				repoDir := setupGuardTestRepo(t)
				hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
				writeNonShellHookForTest(t, hookPath, tc.before)
				staleBackup := "#!/usr/bin/env python3\nprint('stale snapshot')\n"
				writeNonShellHookForTest(t, hookPath+".backup", staleBackup)

				stderr := installHooksCapturingStderr(t, false)
				require.Equal(t, tc.want, readNonShellHookForTest(t, hookPath))
				require.Contains(t, stderr, "removed")
				require.Contains(t, stderr, "bd hooks run pre-push")
				require.Equal(t, staleBackup, readNonShellHookForTest(t, hookPath+".backup"), "the stale backup is neither restored nor changed")

				stderr = installHooksCapturingStderr(t, false)
				require.Equal(t, tc.want, readNonShellHookForTest(t, hookPath), "second install changes nothing")
				require.NotContains(t, stderr, "removed")
			})
		}

		t.Run("preservation_copy", func(t *testing.T) {
			// CRLF: the shell preservation path would normalize the bytes.
			before := crlf + "\r\n" + strings.ReplaceAll(section, "\n", "\r\n")
			want := crlf + "\r\n"
			got, keep := shouldPreserveHookContent(before, false)
			require.True(t, keep)
			require.Equal(t, want, got)

			_, repo := setupSymlinkedHooksDirRepo(t)
			src := filepath.Join(repo, ".git", "hooks", "pre-push")
			writeNonShellHookForTest(t, src, before)
			stderr := installHooksCapturingStderr(t, true)
			require.Equal(t, want, readNonShellHookForTest(t, filepath.Join(repo, ".beads", "hooks", "pre-push")))
			require.Contains(t, stderr, "removed 1 bd shell section(s) from the preserved pre-push hook")
			require.Equal(t, before, readNonShellHookForTest(t, src), "the source hook is not modified by preservation")
		})
	})
}
