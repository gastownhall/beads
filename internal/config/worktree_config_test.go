package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

func setupConfigWorktree(t *testing.T) (mainRepoDir, worktreeDir, mainConfigPath string) {
	t.Helper()

	tmpDir := t.TempDir()
	mainRepoDir = filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0o755); err != nil {
		t.Fatalf("failed to create main repo dir: %v", err)
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")

	readmePath := filepath.Join(mainRepoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("failed to write README.md: %v", err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir = filepath.Join(tmpDir, "worktree")
	addWorktree := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	addWorktree.Dir = mainRepoDir
	if out, err := addWorktree.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		removeWorktree := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		removeWorktree.Dir = mainRepoDir
		_ = removeWorktree.Run()
	})

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatalf("failed to create main .beads dir: %v", err)
	}
	mainConfigPath = filepath.Join(mainBeadsDir, "config.yaml")
	if err := os.WriteFile(mainConfigPath, []byte("no-git-ops: false\n"), 0o644); err != nil {
		t.Fatalf("failed to write main config.yaml: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(worktreeDir, ".beads")); err != nil {
		t.Fatalf("failed to remove worktree .beads: %v", err)
	}

	t.Setenv("BEADS_DIR", "")
	t.Chdir(worktreeDir)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)

	return mainRepoDir, worktreeDir, mainConfigPath
}

func TestSetYamlConfig_WorktreeFallbackUsesMainRepoConfig(t *testing.T) {
	_, worktreeDir, mainConfigPath := setupConfigWorktree(t)

	if err := SetYamlConfig("no-git-ops", "true"); err != nil {
		t.Fatalf("SetYamlConfig() error = %v", err)
	}

	content, err := os.ReadFile(mainConfigPath)
	if err != nil {
		t.Fatalf("failed to read main config.yaml: %v", err)
	}
	if !strings.Contains(string(content), "no-git-ops: true") {
		t.Fatalf("expected shared config.yaml to be updated, got:\n%s", string(content))
	}

	if _, err := os.Stat(filepath.Join(worktreeDir, ".beads", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no worktree-local config.yaml, got err=%v", err)
	}
}

func TestSetYamlConfig_PrefersWorktreeLocalConfigWhenPresent(t *testing.T) {
	_, worktreeDir, mainConfigPath := setupConfigWorktree(t)

	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0o755); err != nil {
		t.Fatalf("failed to create worktree .beads dir: %v", err)
	}
	worktreeConfigPath := filepath.Join(worktreeBeadsDir, "config.yaml")
	if err := os.WriteFile(worktreeConfigPath, []byte("no-git-ops: false\nactor: worktree-user\n"), 0o644); err != nil {
		t.Fatalf("failed to write worktree config.yaml: %v", err)
	}

	if err := SetYamlConfig("no-git-ops", "true"); err != nil {
		t.Fatalf("SetYamlConfig() error = %v", err)
	}

	worktreeContent, err := os.ReadFile(worktreeConfigPath)
	if err != nil {
		t.Fatalf("failed to read worktree config.yaml: %v", err)
	}
	if !strings.Contains(string(worktreeContent), "no-git-ops: true") {
		t.Fatalf("expected worktree config.yaml to be updated, got:\n%s", string(worktreeContent))
	}

	mainContent, err := os.ReadFile(mainConfigPath)
	if err != nil {
		t.Fatalf("failed to read main config.yaml: %v", err)
	}
	if strings.Contains(string(mainContent), "no-git-ops: true") {
		t.Fatalf("expected shared config.yaml to remain unchanged, got:\n%s", string(mainContent))
	}
}

func TestFindConfigYAMLPath_WorktreeFallbackUsesMainRepoConfig(t *testing.T) {
	_, _, mainConfigPath := setupConfigWorktree(t)

	got, err := FindConfigYAMLPath()
	if err != nil {
		t.Fatalf("FindConfigYAMLPath() error = %v", err)
	}

	gotResolved, err := filepath.EvalSymlinks(filepath.Clean(got))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(filepath.Clean(mainConfigPath))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", mainConfigPath, err)
	}

	if gotResolved != wantResolved {
		t.Fatalf("FindConfigYAMLPath() = %q, want %q", gotResolved, wantResolved)
	}
}

func TestInitialize_WorktreeFallbackUsesMainRepoConfig(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	_, _, mainConfigPath := setupConfigWorktree(t)
	if err := os.WriteFile(mainConfigPath, []byte("json: true\nactor: shared-user\n"), 0o644); err != nil {
		t.Fatalf("failed to write main config.yaml: %v", err)
	}

	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if got := GetBool("json"); !got {
		t.Fatalf("GetBool(json) = %v, want true", got)
	}
	if got := GetString("actor"); got != "shared-user" {
		t.Fatalf("GetString(actor) = %q, want %q", got, "shared-user")
	}
}

func TestInitialize_WorktreeFallbackMergesSharedLocalOverride(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	_, _, mainConfigPath := setupConfigWorktree(t)
	if err := os.WriteFile(mainConfigPath, []byte("json: false\nactor: project-user\n"), 0o644); err != nil {
		t.Fatalf("failed to write main config.yaml: %v", err)
	}

	localConfigPath := filepath.Join(filepath.Dir(mainConfigPath), "config.local.yaml")
	if err := os.WriteFile(localConfigPath, []byte("actor: local-user\n"), 0o644); err != nil {
		t.Fatalf("failed to write config.local.yaml: %v", err)
	}

	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if got := GetBool("json"); got {
		t.Fatalf("GetBool(json) = %v, want false", got)
	}
	if got := GetString("actor"); got != "local-user" {
		t.Fatalf("GetString(actor) = %q, want %q", got, "local-user")
	}
}

func TestWorktreeFallbackConfigPath(t *testing.T) {
	mainRepoDir, worktreeDir, mainConfigPath := setupConfigWorktree(t)

	t.Run("shared worktree resolves main repo config", func(t *testing.T) {
		got := worktreeFallbackConfigPath(worktreeDir)
		gotResolved, err := filepath.EvalSymlinks(filepath.Clean(got))
		if err != nil {
			t.Fatalf("EvalSymlinks(%q) failed: %v", got, err)
		}
		wantResolved, err := filepath.EvalSymlinks(filepath.Clean(mainConfigPath))
		if err != nil {
			t.Fatalf("EvalSymlinks(%q) failed: %v", mainConfigPath, err)
		}
		if gotResolved != wantResolved {
			t.Fatalf("worktreeFallbackConfigPath() = %q, want %q", gotResolved, wantResolved)
		}
	})

	t.Run("primary repo returns empty", func(t *testing.T) {
		if got := worktreeFallbackConfigPath(mainRepoDir); got != "" {
			t.Fatalf("worktreeFallbackConfigPath(mainRepoDir) = %q, want empty", got)
		}
	})

	t.Run("non git repo returns empty", func(t *testing.T) {
		if got := worktreeFallbackConfigPath(t.TempDir()); got != "" {
			t.Fatalf("worktreeFallbackConfigPath(non-git) = %q, want empty", got)
		}
	})
}

func TestGitDirsForRepo_NonGitRepo(t *testing.T) {
	if gitDir, commonDir, ok := gitDirsForRepo(t.TempDir()); ok || gitDir != "" || commonDir != "" {
		t.Fatalf("gitDirsForRepo(non-git) = (%q, %q, %v), want empty paths and false", gitDir, commonDir, ok)
	}
}

func TestWorktreePathHelpers_EdgeCases(t *testing.T) {
	t.Run("empty git path", func(t *testing.T) {
		if got := gitPathForRepo(t.TempDir(), ""); got != "" {
			t.Fatalf("gitPathForRepo(empty) = %q, want empty", got)
		}
	})

	t.Run("missing relative git path falls back to clean path", func(t *testing.T) {
		repoPath := t.TempDir()
		want := filepath.Join(repoPath, "missing-git-dir")
		if got := gitPathForRepo(repoPath, "missing-git-dir"); got != want {
			t.Fatalf("gitPathForRepo(missing) = %q, want %q", got, want)
		}
	})

	t.Run("same path handles empty inputs", func(t *testing.T) {
		if !samePath("", "") {
			t.Fatal("samePath(empty, empty) = false, want true")
		}
		if samePath("", t.TempDir()) {
			t.Fatal("samePath(empty, dir) = true, want false")
		}
	})
}

func TestFindProjectConfigYamlWithFinder_UsesLoadedWorktreeConfig(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	_, _, mainConfigPath := setupConfigWorktree(t)
	if err := os.WriteFile(mainConfigPath, []byte("sync.remote: git+ssh://git@example.com/org/repo.git\n"), 0o644); err != nil {
		t.Fatalf("failed to write main config.yaml: %v", err)
	}

	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	got, err := findProjectConfigYamlWithFinder(func() string { return "" })
	if err != nil {
		t.Fatalf("findProjectConfigYamlWithFinder() error = %v", err)
	}

	gotResolved, err := filepath.EvalSymlinks(filepath.Clean(got))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(filepath.Clean(mainConfigPath))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", mainConfigPath, err)
	}

	if gotResolved != wantResolved {
		t.Fatalf("findProjectConfigYamlWithFinder() = %q, want %q", gotResolved, wantResolved)
	}
}

func TestFindProjectConfigYamlWithFinder_UsesSharedConfigAfterLocalOverrideMerge(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	_, _, mainConfigPath := setupConfigWorktree(t)
	if err := os.WriteFile(mainConfigPath, []byte("json: false\nactor: shared-user\n"), 0o644); err != nil {
		t.Fatalf("failed to write main config.yaml: %v", err)
	}

	localConfigPath := filepath.Join(filepath.Dir(mainConfigPath), "config.local.yaml")
	if err := os.WriteFile(localConfigPath, []byte("actor: local-user\n"), 0o644); err != nil {
		t.Fatalf("failed to write config.local.yaml: %v", err)
	}

	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	usedConfig := ConfigFileUsed()
	usedResolved, err := filepath.EvalSymlinks(filepath.Clean(usedConfig))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", usedConfig, err)
	}

	wantResolved, err := filepath.EvalSymlinks(filepath.Clean(mainConfigPath))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", mainConfigPath, err)
	}

	if usedResolved != wantResolved {
		t.Fatalf("ConfigFileUsed() = %q, want %q", usedResolved, wantResolved)
	}

	got, err := findProjectConfigYamlWithFinder(func() string { return "" })
	if err != nil {
		t.Fatalf("findProjectConfigYamlWithFinder() error = %v", err)
	}

	gotResolved, err := filepath.EvalSymlinks(filepath.Clean(got))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", got, err)
	}

	if gotResolved != wantResolved {
		t.Fatalf("findProjectConfigYamlWithFinder() = %q, want %q", gotResolved, wantResolved)
	}
}
