package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

func TestFindBeadsRepoRoot_WorktreeFallback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-worktree-cfg-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
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

	worktreeDir := filepath.Join(tmpDir, "worktree")
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

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	worktreeBeads := filepath.Join(worktreeDir, ".beads")
	os.RemoveAll(worktreeBeads)

	t.Chdir(worktreeDir)
	git.ResetCaches()

	got := findBeadsRepoRoot(worktreeDir)
	if got == "" {
		t.Fatal("findBeadsRepoRoot returned empty string; expected main repo dir")
	}

	gotClean := filepath.Clean(strings.TrimSpace(got))
	wantClean := filepath.Clean(strings.TrimSpace(mainRepoDir))

	gotEval, err := filepath.EvalSymlinks(gotClean)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", gotClean, err)
	}
	wantEval, err := filepath.EvalSymlinks(wantClean)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", wantClean, err)
	}

	if gotEval != wantEval {
		t.Errorf("findBeadsRepoRoot = %q, want %q", gotEval, wantEval)
	}
}

// TestFindBeadsRepoRoot_IgnoresLeakedAncestorBeadsDir guards against a
// regression where a stray .beads directory in an ancestor of the test's own
// temp tree (e.g. a leaked /tmp/.beads left behind by a process that ran bd
// with HOME=/tmp) gets treated as the repo root, short-circuiting the walk
// before it ever reaches the correct git worktree-fallback resolution
// (be-9x3y2). Unlike TestFindBeadsRepoRoot_WorktreeFallback, this test does
// not depend on any incidental leaked state: it plants its own fake ancestor
// .beads dir, so it fails deterministically regardless of the host's /tmp
// contents.
func TestFindBeadsRepoRoot_IgnoresLeakedAncestorBeadsDir(t *testing.T) {
	outerRoot, err := os.MkdirTemp("", "beads-ancestor-leak-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outerRoot) })

	// Stand-in for a leaked /tmp/.beads: a .beads dir at an ancestor of our
	// own test tree that has nothing to do with the "real" repo below.
	if err := os.MkdirAll(filepath.Join(outerRoot, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	tmpDir := filepath.Join(outerRoot, "nested", "beads-worktree-cfg-test")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatal(err)
	}

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
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

	worktreeDir := filepath.Join(tmpDir, "worktree")
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

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(worktreeDir, ".beads"))

	t.Chdir(worktreeDir)
	git.ResetCaches()

	got := findBeadsRepoRoot(worktreeDir)
	if got == "" {
		t.Fatal("findBeadsRepoRoot returned empty string; expected main repo dir")
	}

	gotClean := filepath.Clean(strings.TrimSpace(got))
	wantClean := filepath.Clean(strings.TrimSpace(mainRepoDir))

	gotEval, err := filepath.EvalSymlinks(gotClean)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", gotClean, err)
	}
	wantEval, err := filepath.EvalSymlinks(wantClean)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", wantClean, err)
	}

	if gotEval != wantEval {
		t.Errorf("findBeadsRepoRoot = %q, want %q (leaked ancestor .beads at %q must not shadow the real repo root)", gotEval, wantEval, outerRoot)
	}
}

func TestBeadsPollutionCheck_WorktreeSkips(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-worktree-preflight-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
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

	worktreeDir := filepath.Join(tmpDir, "worktree")
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

	if err := os.MkdirAll(filepath.Join(mainRepoDir, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(worktreeDir, ".beads"))

	t.Chdir(worktreeDir)
	git.ResetCaches()

	result := runBeadsPollutionCheck()
	if !result.Passed {
		t.Errorf("runBeadsPollutionCheck() in worktree: Passed=%v, want true (should skip when .beads is outside worktree)", result.Passed)
	}
}
