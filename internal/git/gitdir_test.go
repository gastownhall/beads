package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupTestRepo creates a temporary git repository for testing.
func setupTestRepo(t *testing.T) (repoPath string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	repoPath = filepath.Join(tmpDir, "test-repo")
	if err := os.MkdirAll(repoPath, 0750); err != nil {
		t.Fatalf("Failed to create test repo directory: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init git repo: %v\nOutput: %s", err, string(output))
	}
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoPath
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoPath
	_ = cmd.Run()
	beadsDir := filepath.Join(repoPath, ".beads")
	_ = os.MkdirAll(beadsDir, 0750)
	_ = os.WriteFile(filepath.Join(beadsDir, "test.jsonl"), []byte("test data\n"), 0644)
	_ = os.WriteFile(filepath.Join(repoPath, "other.txt"), []byte("other data\n"), 0644)
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoPath
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = repoPath
	_, _ = cmd.CombinedOutput()
	cleanup = func() {}
	return repoPath, cleanup
}

func TestGetGitHooksDirTildeExpansion(t *testing.T) {
	// Use an explicit temporary home so tilde expansion is deterministic
	// regardless of the environment (CI, containers, overridden homes, etc.).
	fakeHome := t.TempDir()

	tests := []struct {
		name      string
		hooksPath string
		// wantDir is either an absolute path or "REPO_RELATIVE:" prefix
		// meaning the expected path is relative to the subtest's repo root.
		wantDir string
	}{
		{
			name:      "tilde with forward slash",
			hooksPath: "~/.githooks",
			wantDir:   filepath.Join(fakeHome, ".githooks"),
		},
		{
			name:      "tilde with backslash",
			hooksPath: `~\.githooks`,
			wantDir:   filepath.Join(fakeHome, ".githooks"),
		},
		{
			name:      "bare tilde",
			hooksPath: "~",
			wantDir:   fakeHome,
		},
		{
			name:      "relative path without tilde",
			hooksPath: ".beads/hooks",
			wantDir:   "REPO_RELATIVE:.beads/hooks",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each subtest gets its own repo to avoid git config corruption.
			// Setting core.hooksPath to a backslash-tilde path (e.g. ~\.githooks)
			// causes all subsequent git commands to fail with "failed to expand
			// user dir", and even `git config --unset` cannot recover.
			//
			// IMPORTANT: setupTestRepo must run BEFORE overriding HOME, because
			// git init/commit need the real HOME for global config access
			// (e.g. safe.directory on CI). Overriding HOME too early causes
			// git config to fail with exit status 128 on some environments.
			subRepoPath, subCleanup := setupTestRepo(t)
			defer subCleanup()

			// Override both home variables after repo setup so Git and
			// os.UserHomeDir resolve to fakeHome on every platform.
			t.Setenv("HOME", fakeHome)
			t.Setenv("USERPROFILE", fakeHome)

			ResetCaches()

			cmd := exec.Command("git", "config", "core.hooksPath", tt.hooksPath)
			cmd.Dir = subRepoPath
			if err := cmd.Run(); err != nil {
				t.Skipf("git config rejected core.hooksPath %q: %v", tt.hooksPath, err)
			}

			originalDir, err := os.Getwd()
			if err != nil {
				t.Fatalf("Failed to get working directory: %v", err)
			}
			if err := os.Chdir(subRepoPath); err != nil {
				t.Fatalf("Failed to chdir to test repo: %v", err)
			}
			t.Cleanup(func() { os.Chdir(originalDir) })

			gotDir, err := GetGitHooksDir()
			if err != nil {
				t.Fatalf("GetGitHooksDir() returned error: %v", err)
			}

			wantDir := tt.wantDir
			const repoRelPrefix = "REPO_RELATIVE:"
			if len(wantDir) > len(repoRelPrefix) && wantDir[:len(repoRelPrefix)] == repoRelPrefix {
				wantDir = filepath.Join(subRepoPath, wantDir[len(repoRelPrefix):])
			}

			// On macOS, /var is a symlink to /private/var, so we need to resolve
			// symlinks before comparing paths for equality.
			gotDirResolved, _ := filepath.EvalSymlinks(gotDir)
			wantDirResolved, _ := filepath.EvalSymlinks(wantDir)
			if gotDirResolved != wantDirResolved {
				t.Errorf("GetGitHooksDir() = %q (resolved: %q), want %q (resolved: %q)",
					gotDir, gotDirResolved, wantDir, wantDirResolved)
			}
		})
	}
}

func TestResolveHooksContext(t *testing.T) {
	// Own both Git and Go home resolution; don't borrow user/global settings.
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			t.Cleanup(func() { _ = os.Setenv(key, value) })
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	cleanEnv := os.Environ()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, cleanEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	mkdir := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
		return path
	}
	canonical := func(path string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		return NormalizePath(resolved)
	}
	root := canonical(t.TempDir())
	selected, decoy := mkdir(filepath.Join(root, "selected repo")), mkdir(filepath.Join(root, "decoy repo"))
	for _, repo := range []string{selected, decoy} {
		git(repo, "init")
		git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=", "commit", "--allow-empty", "-m", "seed")
	}
	linked := filepath.Join(root, "linked repo")
	git(selected, "worktree", "add", "-b", "linked", linked)
	nested := mkdir(filepath.Join(selected, "nested dir"))
	nonrepo := mkdir(filepath.Join(root, "nonrepo"))
	bare := mkdir(filepath.Join(root, "bare.git"))
	git(bare, "init", "--bare")
	t.Chdir(decoy)
	ResetCaches()
	t.Cleanup(ResetCaches)
	check := func(t *testing.T, dir string, env []string, repo, hooks string) HooksContext {
		t.Helper()
		got, err := ResolveHooksContext(dir, env)
		fresh, freshErr := GetGitHooksDirFrom(dir, env)
		if freshErr != nil || fresh != hooks {
			t.Errorf("fresh lazy hooks = %q, %v; want %q", fresh, freshErr, hooks)
		}
		want := HooksContext{HooksDir: hooks, CommonDir: filepath.Join(canonical(selected), ".git"), RepoRoot: canonical(repo), MainRepoRoot: canonical(selected)}
		if err != nil || got != want {
			t.Errorf("selected context = %+v, %v; want %+v", got, err, want)
		}
		return got
	}
	for _, tc := range []struct {
		name, dir, repo, configured, want string
		global                            bool
	}{
		{"ordinary", selected, selected, "", filepath.Join(canonical(selected), ".git", "hooks"), false},
		{"nested", nested, selected, "", filepath.Join(canonical(selected), ".git", "hooks"), false},
		{"linked", linked, linked, "", filepath.Join(canonical(selected), ".git", "hooks"), false},
		{"relative", linked, linked, "custom/hooks", filepath.Join(canonical(linked), "custom", "hooks"), false},
		{"absolute", nested, selected, filepath.Join(home, "absolute hooks"), filepath.Join(home, "absolute hooks"), false},
		{"tilde", selected, selected, "~/tilde hooks", filepath.Join(home, "tilde hooks"), false},
		{"global", selected, selected, filepath.Join(home, "global hooks"), filepath.Join(home, "global hooks"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.configured != "" {
				scope := "--local"
				if tc.global {
					scope = "--global"
				}
				git(selected, "config", scope, "core.hooksPath", tc.configured)
				defer git(selected, "config", scope, "--unset", "core.hooksPath")
			}
			check(t, tc.dir, cleanEnv, tc.repo, tc.want)
			t.Chdir(tc.dir)
			ResetCaches()
			legacy, err := GetGitHooksDir()
			if err != nil || legacy != tc.want {
				t.Fatalf("legacy hooks = %q, %v; want %q", legacy, err, tc.want)
			}
		})
	}
	t.Run("relative_workdir", func(t *testing.T) {
		relative, err := filepath.Rel(decoy, nested)
		if err != nil {
			t.Fatal(err)
		}
		check(t, relative, cleanEnv, selected, filepath.Join(canonical(selected), ".git", "hooks"))
	})
	t.Run("routing_environment", func(t *testing.T) {
		t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
		t.Setenv("GIT_WORK_TREE", decoy)
		for _, env := range [][]string{nil, os.Environ()} {
			got, err := ResolveHooksContext(selected, env)
			if err != nil || got.RepoRoot != canonical(decoy) {
				t.Fatalf("supplied/inherited routing = %+v, %v", got, err)
			}
		}
		check(t, selected, cleanEnv, selected, filepath.Join(canonical(selected), ".git", "hooks"))
	})
	t.Run("empty_environment", func(t *testing.T) {
		localHooks, ambientHooks := filepath.Join(home, "local"), filepath.Join(home, "inline")
		git(selected, "config", "core.hooksPath", localHooks)
		defer git(selected, "config", "--unset", "core.hooksPath")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
		t.Setenv("GIT_CONFIG_VALUE_0", ambientHooks)
		check(t, selected, nil, selected, ambientHooks)
		check(t, selected, []string{}, selected, localHooks)
	})
	t.Run("process_tilde_home", func(t *testing.T) {
		git(selected, "config", "core.hooksPath", "~/process hooks")
		defer git(selected, "config", "--unset", "core.hooksPath")
		childEnv := append(append([]string{}, cleanEnv...), "HOME="+t.TempDir())
		check(t, selected, childEnv, selected, filepath.Join(home, "process hooks"))
	})
	for _, cachedFailure := range []bool{false, true} {
		name := "cached_success"
		if cachedFailure {
			name = "cached_failure"
		}
		t.Run(name, func(t *testing.T) {
			if cachedFailure {
				t.Chdir(nonrepo)
			}
			ResetCaches()
			_, err := getGitContext()
			if (err != nil) != cachedFailure {
				t.Fatalf("cache precondition: %v", err)
			}
			before := gitCtx
			check(t, selected, cleanEnv, selected, filepath.Join(canonical(selected), ".git", "hooks"))
			if gitCtx != before {
				t.Fatal("explicit context changed legacy cache")
			}
		})
	}
	t.Run("legacy_absolute_after_cached_failure", func(t *testing.T) {
		t.Chdir(nonrepo)
		ResetCaches()
		if _, err := getGitContext(); err == nil {
			t.Fatal("nonrepo cache precondition")
		}
		absolute := filepath.Join(home, "global outside repository")
		git(selected, "config", "--global", "core.hooksPath", absolute)
		defer git(selected, "config", "--global", "--unset", "core.hooksPath")
		got, err := GetGitHooksDir()
		if err != nil || got != absolute {
			t.Fatalf("lazy absolute hooks = %q, %v; want %q", got, err, absolute)
		}
		for _, dir := range []string{nonrepo, bare} {
			fresh, err := GetGitHooksDirFrom(dir, cleanEnv)
			if err != nil || fresh != absolute {
				t.Errorf("fresh absolute hooks from %q = %q, %v", dir, fresh, err)
			}
		}
	})
	for name, dir := range map[string]string{"empty_workdir": "", "nonrepo": nonrepo, "bare": bare} {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveHooksContext(dir, cleanEnv)
			if err == nil || got != (HooksContext{}) {
				t.Fatalf("invalid context = %+v, %v", got, err)
			}
			if name == "bare" && !strings.Contains(err.Error(), "work tree") {
				t.Errorf("bare repository error must retain Git's work-tree diagnostic: %v", err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "selected alias")
		if err := os.Symlink(selected, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink capability unavailable: %v", err)
			}
			t.Fatal(err)
		}
		check(t, link, cleanEnv, selected, filepath.Join(canonical(selected), ".git", "hooks"))
	})
}
