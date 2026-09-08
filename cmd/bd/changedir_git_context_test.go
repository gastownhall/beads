package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo creates a real git repository with an initial commit so
// rev-parse --absolute-git-dir resolves. No mocks: the git context fix under
// test is exactly the exec-level contract (bd-6m4).
func initTestRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatalf("rev-parse in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

func TestRetargetGitContextPointsGitEnvAtTargetRepo(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	initTestRepo(t, dirA)
	gitDirB := initTestRepo(t, dirB)

	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_WORK_TREE", "")
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dirA); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	if err := retargetGitContext(dirB); err != nil {
		t.Fatalf("retargetGitContext(%q): %v", dirB, err)
	}
	if got := os.Getenv("GIT_DIR"); got != gitDirB {
		t.Errorf("GIT_DIR = %q, want target repo git dir %q (bd-6m4: -C must retarget the git context)", got, gitDirB)
	}
	if got := os.Getenv("GIT_WORK_TREE"); !filepath.IsAbs(got) || !strings.HasSuffix(got, filepath.Base(dirB)) {
		t.Errorf("GIT_WORK_TREE = %q, want an absolute path inside the target repo", got)
	}
	// Decisive observable: plain git now resolves the TARGET repo, not the
	// caller's cwd — this is what makes hooks install land in the -C target.
	out, err := exec.Command("git", "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != gitDirB {
		t.Errorf("git rev-parse resolved %q, want %q", got, gitDirB)
	}
}

func TestRetargetGitContextNonRepoIsNoOp(t *testing.T) {
	dir := t.TempDir() // no git repository here
	if err := retargetGitContext(dir); err != nil {
		t.Fatalf("retargetGitContext on non-repo must be a no-op, got: %v", err)
	}
	if got := os.Getenv("GIT_DIR"); got != "" {
		t.Errorf("GIT_DIR = %q, want untouched on non-repo target", got)
	}
}
