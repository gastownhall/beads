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
	"github.com/steveyegge/beads/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestConfigureBeadsHooksPath_WorktreeUsesMainRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-hooks-worktree-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepoDir
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	if err := configureBeadsHooksPath(); err != nil {
		t.Fatalf("configureBeadsHooksPath failed: %v", err)
	}

	cmd = exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = mainRepoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get core.hooksPath failed: %v", err)
	}
	hooksPath := filepath.Clean(strings.TrimSpace(string(out)))
	expected := filepath.Join(mainRepoDir, ".beads", "hooks")
	hooksPath, _ = filepath.EvalSymlinks(hooksPath)
	expected, _ = filepath.EvalSymlinks(expected)
	if hooksPath != expected {
		t.Errorf("core.hooksPath = %q, want %q", hooksPath, expected)
	}
}

func TestConfigureSharedHooksPath_WorktreeUsesMainRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-shared-hooks-worktree-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepoDir
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads-hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	if err := configureSharedHooksPath(); err != nil {
		t.Fatalf("configureSharedHooksPath failed: %v", err)
	}

	cmd = exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = mainRepoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get core.hooksPath failed: %v", err)
	}
	hooksPath := filepath.Clean(strings.TrimSpace(string(out)))
	expected := filepath.Join(mainRepoDir, ".beads-hooks")
	hooksPath, _ = filepath.EvalSymlinks(hooksPath)
	expected, _ = filepath.EvalSymlinks(expected)
	if hooksPath != expected {
		t.Errorf("core.hooksPath = %q, want %q", hooksPath, expected)
	}
}

func TestResetHooksPathIfBeadsManaged_Worktree(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-reset-hooks-worktree-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainRepoDir
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	hooksPathToSet := filepath.Join(mainRepoDir, ".beads", "hooks")
	evaluated, _ := filepath.EvalSymlinks(hooksPathToSet)
	if evaluated != "" {
		hooksPathToSet = evaluated
	}
	cmd = exec.Command("git", "config", "core.hooksPath", hooksPathToSet)
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config core.hooksPath failed: %v", err)
	}

	run("config", "beads.role", "primary")
	run("config", "extensions.worktreeConfig", "true")
	cmd = exec.Command("git", "config", "--worktree", "beads.role", "worktree-only")
	cmd.Dir = worktreeDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure worktree role: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "config", "--worktree", "core.hooksPath", ".beads/hooks")
	cmd.Dir = worktreeDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure worktree hooksPath: %v\n%s", err, out)
	}
	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	git.ResetCaches() // Exercise the actual reset with a cold context.
	if err := resetHooksPathIfBeadsManaged(); err != nil {
		t.Fatalf("resetHooksPathIfBeadsManaged failed: %v", err)
	}

	cmd = exec.Command("git", "config", "--local", "--get", "core.hooksPath")
	cmd.Dir = mainRepoDir
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("core.hooksPath = %q after reset, want empty", strings.TrimSpace(string(out)))
	}
	cmd = exec.Command("git", "config", "--local", "--get", "beads.role")
	cmd.Dir = mainRepoDir
	out, err = cmd.Output()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Errorf("main role remains after reset: %q, %v", out, err)
	}
	cmd = exec.Command("git", "config", "--worktree", "--get", "beads.role")
	cmd.Dir = worktreeDir
	out, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "worktree-only" {
		t.Errorf("worktree-specific role changed: %q, %v", out, err)
	}
	cmd = exec.Command("git", "config", "--worktree", "--get", "core.hooksPath")
	cmd.Dir = worktreeDir
	out, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != ".beads/hooks" {
		t.Errorf("worktree-specific hooksPath changed: %q, %v", out, err)
	}
	t.Run("preserve_worktree_role", func(t *testing.T) {
		// Seed a new common role so this reset proves removal independently of the first call.
		run("config", "--local", "beads.role", "primary")
		privateDir, err := git.GetGitDir()
		if err != nil {
			t.Fatal(err)
		}
		privateConfig := filepath.Join(privateDir, "config.worktree")
		before, err := os.ReadFile(privateConfig)
		if err != nil {
			t.Fatal(err)
		}
		git.ResetCaches()
		t.Cleanup(git.ResetCaches)
		if err := resetHooksPathIfBeadsManaged(); err != nil {
			t.Fatalf("reset while preserving a worktree role failed: %v", err)
		}
		if after, err := os.ReadFile(privateConfig); err != nil || string(after) != string(before) {
			t.Fatalf("private worktree config changed: %v", err)
		}
		get := exec.Command("git", "config", "--local", "--get", "beads.role")
		get.Dir = mainRepoDir
		out, err := get.Output()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Errorf("common role after reset = %q, %v; want absent", out, err)
		}
	})
}

func TestConfigureBeadsHooksPath_NormalRepoUnchanged(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-hooks-normal-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Skipf("git %v failed: %v", args, err)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "Initial commit")

	if err := os.MkdirAll(filepath.Join(repoDir, ".beads", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(repoDir)
	git.ResetCaches()

	if git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return false in normal repo")
	}

	if err := configureBeadsHooksPath(); err != nil {
		t.Fatalf("configureBeadsHooksPath failed: %v", err)
	}

	cmd := exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get core.hooksPath failed: %v", err)
	}
	hooksPath := filepath.Clean(strings.TrimSpace(string(out)))
	expected := filepath.Join(repoDir, ".beads", "hooks")
	hooksPath, _ = filepath.EvalSymlinks(hooksPath)
	expected, _ = filepath.EvalSymlinks(expected)
	if hooksPath != expected {
		t.Errorf("core.hooksPath = %q, want %q", hooksPath, expected)
	}
}

// newInitHooksFixture keeps the selected worktree, ambient repository and
// storage physically distinct. Git's default HOME is owned by the parent helper.
func newInitHooksFixture(t *testing.T) (selected, decoy, storage, common string) {
	t.Helper()
	selected, decoy, exclude, _ := newInitExcludeRepos(t)
	common = filepath.Dir(filepath.Dir(exclude))
	storage = filepath.Join(t.TempDir(), "separate storage")
	require.NoError(t, os.Mkdir(storage, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(decoy, "seed"), []byte("decoy\n"), 0600))
	initExcludeGit(t, decoy, "add", "seed")
	initExcludeGit(t, selected, "config", "beads.role", "contributor")
	t.Setenv("BEADS_DIR", filepath.Join(decoy, ".beads"))
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	return selected, decoy, storage, common
}

func readInitHooksFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func preserveStandaloneHookInputs(t *testing.T, paths ...string) {
	t.Helper()
	beforeEnv := os.Environ()
	t.Cleanup(func() { require.Equal(t, beforeEnv, os.Environ()) })
	for _, path := range paths {
		before := readInitHooksFile(t, path)
		t.Cleanup(func() { require.Equal(t, before, readInitHooksFile(t, path), "changed %s", path) })
	}
}

func setStandaloneHookMode(t *testing.T, mode string) {
	t.Helper()
	oldJSON := jsonOutput
	jsonOutput = false // These command fixtures assert the human-readable stderr contract.
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, flag := range []string{"force", "shared", "chain", "beads"} {
		f := hooksInstallCmd.Flags().Lookup(flag)
		old, changed := f.Value.String(), f.Changed
		t.Cleanup(func() {
			require.NoError(t, hooksInstallCmd.Flags().Set(flag, old))
			f.Changed = changed
		})
		value := "false"
		if flag == mode {
			value = "true"
		}
		require.NoError(t, hooksInstallCmd.Flags().Set(flag, value))
	}
}

func TestStandaloneHookCommandsUseSelectedContext(t *testing.T) {
	for _, name := range []string{"regular", "linked", "private", "shared", "beads", "bare_external", "inline", "config_file", "config_lock"} {
		t.Run(name, func(t *testing.T) {
			selected, decoy, storage, common := newInitHooksFixture(t)
			require.True(t, utils.PathsEqual(decoy, git.GetRepoRoot()), "seed stale decoy cache")
			cwd, mainRoot := decoy, filepath.Dir(common)
			if name == "regular" {
				selected = mainRoot
			}
			private := initExcludeGit(t, selected, "rev-parse", "--absolute-git-dir")
			if name == "bare_external" {
				root := t.TempDir()
				common, selected = filepath.Join(root, "selected bare.git"), filepath.Join(root, "external tree")
				initExcludeGit(t, root, "init", "--bare", common)
				require.NoError(t, os.Mkdir(selected, 0755))
				cwd, mainRoot, private = selected, selected, filepath.Join("..", "selected bare.git")
			}
			t.Chdir(cwd)
			t.Setenv("GIT_DIR", private)
			t.Setenv("GIT_WORK_TREE", selected)
			t.Setenv("BEADS_DIR", storage)
			require.NoError(t, os.WriteFile(filepath.Join(storage, "metadata.json"), []byte("{}\n"), 0600))
			destination := filepath.Join(mainRoot, ".beads", "hooks")
			initExcludeGit(t, cwd, "--git-dir", common, "config", "--local", "core.hooksPath", destination)
			initExcludeGit(t, cwd, "--git-dir", common, "config", "--local", "beads.role", "contributor")
			if name == "private" {
				destination = t.TempDir()
				initExcludeGit(t, selected, "config", "extensions.worktreeConfig", "true")
				initExcludeGit(t, selected, "config", "--worktree", "core.hooksPath", destination)
				preserveStandaloneHookInputs(t, filepath.Join(private, "config.worktree"))
			}
			if name == "shared" {
				destination = filepath.Join(mainRoot, ".beads-hooks")
			} else if name == "beads" {
				destination = filepath.Join(storage, "hooks")
			}
			decoyHook := filepath.Join(decoy, ".git", "hooks", "pre-commit")
			require.NoError(t, os.WriteFile(decoyHook, []byte("#!/bin/sh\n"+generateHookSection("pre-commit")), 0755))
			if name == "inline" {
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
				t.Setenv("GIT_CONFIG_VALUE_0", filepath.Dir(decoyHook))
			}
			if name == "config_file" {
				initExcludeGit(t, decoy, "config", "--local", "core.hooksPath", filepath.Dir(decoyHook))
				t.Setenv("GIT_CONFIG", filepath.Join(decoy, ".git", "config"))
			}
			preserveStandaloneHookInputs(t, decoyHook, filepath.Join(decoy, ".git", "config"), filepath.Join(decoy, ".git", "index"))
			setStandaloneHookMode(t, name)
			require.NoError(t, hooksInstallCmd.RunE(hooksInstallCmd, nil))
			require.Contains(t, string(readInitHooksFile(t, filepath.Join(destination, "pre-commit"))), hookSectionBeginPrefix)
			if name == "shared" || name == "beads" {
				got := initExcludeGit(t, cwd, "--git-dir", common, "config", "--local", "--get", "core.hooksPath")
				require.True(t, utils.PathsEqual(destination, got), "selected hooks path = %q, want %q", got, destination)
			}
			if name == "config_lock" {
				require.NoError(t, os.WriteFile(filepath.Join(common, "config.lock"), []byte("owned lock"), 0600))
				var err error
				stderr := captureStderr(t, func() { err = hooksUninstallCmd.RunE(hooksUninstallCmd, nil) })
				require.Equal(t, &exitError{Code: 1}, err)
				require.Contains(t, stderr, "failed to reset")
				return
			}
			require.NoError(t, hooksUninstallCmd.RunE(hooksUninstallCmd, nil))
			_, err := os.Stat(filepath.Join(destination, "pre-commit"))
			require.ErrorIs(t, err, os.ErrNotExist)
			query := exec.Command("git", "--git-dir", common, "config", "--local", "--get", "beads.role")
			query.Dir, query.Env = cwd, gitenv.ScrubRouting(os.Environ())
			var exit *exec.ExitError
			require.ErrorAs(t, query.Run(), &exit)
			require.Equal(t, 1, exit.ExitCode(), "selected local role must be absent")
			if name == "beads" {
				got := initExcludeGit(t, cwd, "--git-dir", common, "config", "--local", "--get", "core.hooksPath")
				require.True(t, utils.PathsEqual(destination, got), "outside-storage predicate remains conservative")
			}
			require.True(t, utils.PathsEqual(decoy, git.GetRepoRoot()), "fresh commands must leave the legacy cache untouched")
		})
	}
}

func TestInitHooksPreservesManagedDirectoryAliases(t *testing.T) {
	for _, managed := range []string{".beads/hooks", ".beads-hooks"} {
		t.Run(managed, func(t *testing.T) {
			selected, _, storage, common := newInitHooksFixture(t)
			root := filepath.Dir(common)
			source := filepath.Join(root, managed)
			require.NoError(t, os.MkdirAll(source, 0755))
			const content = "#!/bin/sh\necho existing managed-directory hook\n"
			require.NoError(t, os.WriteFile(filepath.Join(source, "post-rewrite"), []byte(content), 0755))
			alias := filepath.Join(t.TempDir(), "repository alias")
			if err := os.Symlink(root, alias); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("directory symlink capability unavailable: %v", err)
				}
				t.Fatal(err)
			}
			initExcludeGit(t, selected, "config", "--local", "core.hooksPath", filepath.Join(alias, managed))
			fs, _, err := withInitHooks(nil, selected, storage)
			require.NoError(t, err)
			require.NoError(t, fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, BeadsHooks: true}))
			_, err = os.Stat(filepath.Join(storage, "hooks", "post-rewrite"))
			require.ErrorIs(t, err, os.ErrNotExist, "an existing managed hooks directory must not be copied through an alias")
			require.Equal(t, content, string(readInitHooksFile(t, filepath.Join(source, "post-rewrite"))))
		})
	}
}

func TestInitHooksRefusesSharedMode(t *testing.T) {
	selected, _, storage, common := newInitHooksFixture(t)
	fs, _, err := withInitHooks(nil, selected, storage)
	require.NoError(t, err)
	require.ErrorContains(t, fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, Shared: true}), "shared hooks mode")
	for _, path := range []string{filepath.Join(storage, "hooks"), filepath.Join(filepath.Dir(common), ".beads-hooks")} {
		_, err := os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestInitHooksContextPreservesSelectedPaths(t *testing.T) {
	for _, name := range []string{"foreign", "global", "private", "config_lock"} {
		t.Run(name, func(t *testing.T) {
			selected, decoy, storage, common := newInitHooksFixture(t)
			current := t.TempDir()
			const foreign = "#!/bin/sh\necho selected hook\n"
			for _, hook := range []string{"pre-commit", "post-rewrite"} {
				require.NoError(t, os.WriteFile(filepath.Join(current, hook), []byte(foreign), 0755))
			}
			initExcludeGit(t, selected, "config", "--local", "core.hooksPath", current)
			if name == "global" {
				initExcludeGit(t, selected, "config", "--local", "--unset", "core.hooksPath")
				initExcludeGit(t, selected, "config", "--global", "core.hooksPath", current)
			}
			preserved := map[string][]byte{}
			if name == "private" {
				initExcludeGit(t, selected, "config", "extensions.worktreeConfig", "true")
				initExcludeGit(t, selected, "config", "--worktree", "core.hooksPath", current)
				path := filepath.Join(initExcludeGit(t, selected, "rev-parse", "--absolute-git-dir"), "config.worktree")
				preserved[path] = readInitHooksFile(t, path)
			}
			for _, path := range []string{filepath.Join(decoy, ".git", "config"), filepath.Join(decoy, ".git", "index")} {
				preserved[path] = readInitHooksFile(t, path)
			}
			if name == "global" {
				path := filepath.Join(os.Getenv("HOME"), ".gitconfig")
				preserved[path] = readInitHooksFile(t, path)
			}
			fs, hooks, err := withInitHooks(nil, selected, storage)
			require.NoError(t, err)
			require.False(t, hooks.installed())
			if name == "config_lock" {
				require.NoError(t, os.WriteFile(filepath.Join(common, "config.lock"), []byte("owned lock"), 0600))
				preserved[filepath.Join(common, "config")] = readInitHooksFile(t, filepath.Join(common, "config"))
			}
			// Later ambient routing must not rebind the captured hook operation.
			t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
			t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(decoy, "wrong hooks"))
			env := os.Environ()
			destination := filepath.Join(storage, "hooks")
			for range 2 {
				err := fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, BeadsHooks: true})
				if name == "config_lock" {
					require.ErrorContains(t, err, "failed to configure git hooks path")
				} else {
					require.NoError(t, err)
					got := initExcludeGit(t, selected, "config", "--file", filepath.Join(common, "config"), "--get", "core.hooksPath")
					gotInfo, err := os.Stat(got)
					require.NoError(t, err)
					wantInfo, err := os.Stat(destination)
					require.NoError(t, err)
					require.True(t, os.SameFile(gotInfo, wantInfo), "common config must name the installed storage hooks")
				}
				installed := string(readInitHooksFile(t, filepath.Join(destination, "pre-commit")))
				require.Contains(t, installed, foreign)
				require.Equal(t, 1, strings.Count(installed, hookSectionBeginPrefix))
				require.Equal(t, foreign, string(readInitHooksFile(t, filepath.Join(destination, "pre-commit.backup"))))
				require.Equal(t, foreign, string(readInitHooksFile(t, filepath.Join(destination, "post-rewrite"))))
			}
			for path, before := range preserved {
				require.Equal(t, before, readInitHooksFile(t, path), "changed %s", path)
			}
			for _, hook := range []string{"pre-commit", "post-rewrite"} {
				require.Equal(t, foreign, string(readInitHooksFile(t, filepath.Join(current, hook))))
			}
			if name == "private" {
				paths, err := git.ResolveHooksContext(selected, hooks.env)
				require.NoError(t, err)
				require.Equal(t, current, paths.HooksDir, "private override remains effective")
			}
			require.Equal(t, env, os.Environ())
		})
	}
}
