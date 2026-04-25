package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupWorktreeRepo(t *testing.T) (mainRepoDir, worktreeDir string) {
	t.Helper()
	isolateGlobalGitConfig(t)

	tmpDir, err := os.MkdirTemp("", "beads-doctor-worktree-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir = filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir = filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cleanupCmd.Dir = mainRepoDir
		_ = cleanupCmd.Run()
	})

	return mainRepoDir, worktreeDir
}

func resolve(t testing.TB, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

func TestResolveBeadsDirForRepo_WorktreeFallback(t *testing.T) {
	mainRepoDir, worktreeDir := setupWorktreeRepo(t)

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(worktreeDir, ".beads"))

	clearResolveBeadsDirCache()

	got := resolve(t, ResolveBeadsDirForRepo(worktreeDir))
	want := resolve(t, mainBeadsDir)

	if got != want {
		t.Errorf("ResolveBeadsDirForRepo(worktree) = %q, want %q", got, want)
	}
}

func TestResolveBeadsDirForRepo_WorktreeLocalBeadsPreferred(t *testing.T) {
	mainRepoDir, worktreeDir := setupWorktreeRepo(t)

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	clearResolveBeadsDirCache()

	got := resolve(t, ResolveBeadsDirForRepo(worktreeDir))
	want := resolve(t, worktreeBeadsDir)

	if got != want {
		t.Errorf("ResolveBeadsDirForRepo(worktree) = %q, want %q (local .beads should take precedence)", got, want)
	}
}

func TestResolveBeadsDirForRepo_WorktreeNoBeadsAnywhere(t *testing.T) {
	_, worktreeDir := setupWorktreeRepo(t)

	os.Remove(filepath.Join(worktreeDir, ".beads"))

	clearResolveBeadsDirCache()

	got := ResolveBeadsDirForRepo(worktreeDir)

	gotClean := filepath.Clean(strings.TrimSpace(got))
	wantBeads := filepath.Join(worktreeDir, ".beads")
	wantClean := filepath.Clean(wantBeads)

	gotResolved, _ := filepath.EvalSymlinks(gotClean)
	wantResolved, _ := filepath.EvalSymlinks(wantClean)

	if gotResolved != wantResolved {
		t.Errorf("ResolveBeadsDirForRepo(worktree with no .beads) = %q, want %q (falls back to local path)", gotResolved, wantResolved)
	}
}
