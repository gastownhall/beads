package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/domain"
	storagefs "github.com/steveyegge/beads/internal/storage/fs"
	storagegit "github.com/steveyegge/beads/internal/storage/git"
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

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	if err := resetHooksPathIfBeadsManaged(); err != nil {
		t.Fatalf("resetHooksPathIfBeadsManaged failed: %v", err)
	}

	cmd = exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir = mainRepoDir
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("core.hooksPath = %q after reset, want empty", strings.TrimSpace(string(out)))
	}
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
	for _, name := range []string{"foreign", "global", "private", "quiet", "config_lock"} {
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
			hooks.quiet = name == "quiet"
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
				var installErr error
				stderr := captureStderr(t, func() {
					installErr = fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, BeadsHooks: true})
				})
				if name == "config_lock" {
					require.ErrorContains(t, installErr, "failed to configure git hooks path")
					require.Empty(t, stderr)
				} else {
					require.NoError(t, installErr)
					if name == "private" {
						require.Contains(t, stderr, "are inactive in this worktree")
						require.Contains(t, stderr, current)
						require.Contains(t, stderr, destination)
						require.Equal(t, current, hooks.paths.HooksDir)
					} else {
						require.Empty(t, stderr)
						require.Equal(t, destination, hooks.paths.HooksDir, "status must use the newly effective destination")
					}
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

func TestInitHooksPrivatePathAlreadyActive(t *testing.T) {
	for _, name := range []string{"same", "alias"} {
		t.Run(name, func(t *testing.T) {
			selected, _, storage, _ := newInitHooksFixture(t)
			destination := filepath.Join(storage, "hooks")
			require.NoError(t, os.MkdirAll(destination, 0755))
			private := destination
			if name == "alias" {
				private = filepath.Join(t.TempDir(), "active hooks alias")
				if err := os.Symlink(destination, private); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("directory symlink capability unavailable: %v", err)
					}
					t.Fatal(err)
				}
			}
			initExcludeGit(t, selected, "config", "extensions.worktreeConfig", "true")
			initExcludeGit(t, selected, "config", "--worktree", "core.hooksPath", private)
			privateConfig := filepath.Join(initExcludeGit(t, selected, "rev-parse", "--absolute-git-dir"), "config.worktree")
			before := readInitHooksFile(t, privateConfig)
			fs, hooks, err := withInitHooks(nil, selected, storage)
			require.NoError(t, err)
			stderr := captureStderr(t, func() {
				require.NoError(t, fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, BeadsHooks: true}))
			})
			require.Empty(t, stderr, "equivalent private path must not report inactive hooks")
			require.Equal(t, before, readInitHooksFile(t, privateConfig))
			require.Equal(t, private, hooks.paths.HooksDir)
			require.Contains(t, string(readInitHooksFile(t, filepath.Join(private, "pre-commit"))), hookSectionBeginPrefix)
		})
	}
}

func TestInitHooksActivationObservationFailure(t *testing.T) {
	selected, _, storage, common := newInitHooksFixture(t)
	fs, hooks, err := withInitHooks(nil, selected, storage)
	require.NoError(t, err)
	destination := filepath.Join(storage, "hooks")
	require.NoError(t, fs.InstallGitHooks(t.Context(), domain.HooksInstallParams{HookNames: managedHookNames, BeadsHooks: true}))
	before := hooks.paths
	installed := readInitHooksFile(t, filepath.Join(destination, "pre-commit"))
	// The write already succeeded; make a later real Git observation fail.
	configPath := filepath.Join(common, "config")
	const invalidConfig = "[broken config\n"
	require.NoError(t, os.WriteFile(configPath, []byte(invalidConfig), 0600))
	stderr := captureStderr(t, func() { hooks.reportHooksActivation(destination) })
	require.Contains(t, stderr, "hooks were installed at "+destination)
	require.Contains(t, stderr, "activation could not be verified")
	require.NotContains(t, stderr, "are inactive")
	require.Equal(t, before, hooks.paths, "failed observation must preserve captured selection and prior status")
	require.Equal(t, installed, readInitHooksFile(t, filepath.Join(destination, "pre-commit")))
	require.Equal(t, invalidConfig, string(readInitHooksFile(t, configPath)), "observation must not rewrite config")
}

func TestProxiedInitHooksActivationHonorsQuiet(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		name := "noisy"
		if quiet {
			name = "quiet"
		}
		t.Run(name, func(t *testing.T) {
			selected, decoy, storage, common := newInitHooksFixture(t)
			private := t.TempDir()
			initExcludeGit(t, selected, "config", "extensions.worktreeConfig", "true")
			initExcludeGit(t, selected, "config", "--worktree", "core.hooksPath", private)
			privateConfig := filepath.Join(initExcludeGit(t, selected, "rev-parse", "--absolute-git-dir"), "config.worktree")
			before := readInitHooksFile(t, privateConfig)
			decoyConfig := readInitHooksFile(t, filepath.Join(decoy, ".git", "config"))
			fs := storagefs.NewFileSystemProvider(selected, newBeadsDirTemplates(), newInitFileSystemAdapters(selected)).BeadsDirFSUseCase()
			cmd := &cobra.Command{}
			cmd.Flags().Bool("setup-exclude", true, "")
			in := initProxiedServerInput{quiet: quiet, skipAgents: true, nonInteractive: true}
			stderr := captureStderr(t, func() {
				require.NoError(t, runInitProxiedServerTail(cmd, t.Context(), in, runInitTailContext{workDir: selected, beadsDir: storage, remoteURL: "file:///unused-hooks-fixture-remote", fsUseCase: fs, gitUC: storagegit.NewGitProvider(selected).GitUseCase()}))
			})
			destination := filepath.Join(storage, "hooks")
			if quiet {
				require.Empty(t, stderr)
			} else {
				require.Contains(t, stderr, "are inactive in this worktree")
				require.Contains(t, stderr, destination)
				require.Contains(t, stderr, private)
				require.NotContains(t, stderr, "Private worktree")
			}
			require.Contains(t, string(readInitHooksFile(t, filepath.Join(destination, "pre-commit"))), hookSectionBeginPrefix)
			require.Equal(t, destination, initExcludeGit(t, selected, "config", "--file", filepath.Join(common, "config"), "--get", "core.hooksPath"))
			require.Equal(t, before, readInitHooksFile(t, privateConfig))
			require.Equal(t, decoyConfig, readInitHooksFile(t, filepath.Join(decoy, ".git", "config")))
			require.Equal(t, private, initExcludeGit(t, selected, "config", "--get", "core.hooksPath"))
		})
	}
}
