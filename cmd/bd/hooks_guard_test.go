package main

import (
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

func TestStandaloneHookCommandKeepsTrackedGuard(t *testing.T) {
	for _, mode := range []string{"default", "shared"} {
		t.Run(mode, func(t *testing.T) {
			_, _, commonExclude, _ := newInitExcludeRepos(t)
			target := filepath.Dir(filepath.Dir(filepath.Dir(commonExclude)))
			t.Chdir(target)
			hooksDir := filepath.Join(target, ".beads-hooks")
			require.NoError(t, os.Mkdir(hooksDir, 0755))
			path := filepath.Join(hooksDir, "pre-commit")
			const foreign = "#!/bin/sh\necho tracked owner\n"
			require.NoError(t, os.WriteFile(path, []byte(foreign), 0755))
			initExcludeGit(t, target, "add", ".beads-hooks/pre-commit")
			initExcludeGit(t, target, "config", "--local", "core.hooksPath", hooksDir)
			setStandaloneHookMode(t, mode)
			var err error
			stderr := captureStderr(t, func() { err = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
			if mode == "shared" {
				require.NoError(t, err)
				require.Contains(t, string(readInitHooksFile(t, path)), foreign)
				require.Contains(t, string(readInitHooksFile(t, path)), hookSectionBeginPrefix)
			} else {
				require.Equal(t, &exitError{Code: 1}, err)
				require.Contains(t, stderr, "tracked by git")
				require.Equal(t, foreign, string(readInitHooksFile(t, path)))
				require.NoFileExists(t, filepath.Join(hooksDir, "post-merge"))
			}
		})
	}
}

func TestGuardHookWritePathIgnoresInheritedGitRouting(t *testing.T) {
	runGit := func(repo string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
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
		{"repository", map[string]string{"GIT_DIR": filepath.Join(decoy, ".git")}},
		{"worktree", map[string]string{"GIT_DIR": filepath.Join(decoy, ".git"), "GIT_WORK_TREE": decoy}},
		{"index", map[string]string{"GIT_INDEX_FILE": filepath.Join(decoy, ".git", "index")}},
		{"inline_config", map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": decoy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if !isGitTrackedFile(foreign) {
				t.Error("inherited routing hid the tracked hook")
			}
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
		cmd := exec.Command("git", args...)
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
		cmd := exec.Command("git", args...)
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
		cmd := exec.Command("git", args...)
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
				cmd := exec.Command("git", args...)
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
			clean := exec.Command("git", args...)
			clean.Env = gitenv.ScrubRouting(inherited)
			if out, err := clean.CombinedOutput(); err == nil {
				t.Fatalf("fixture must require inherited context, scrubbed probe succeeded: %s", out)
			}
			fallback := exec.Command("git", args...)
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
				require.Equal(t, content, string(readInitHooksFile(t, hook)))
				for _, other := range []string{"post-merge", "pre-commit.backup"} {
					_, err := os.Lstat(filepath.Join(hooksDir, other))
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			}
		})
	}
}
