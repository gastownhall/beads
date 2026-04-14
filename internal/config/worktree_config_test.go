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
