package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// initChangeDirTestRepo creates a real git repository with an initial commit
// so rev-parse resolves it, and returns its absolute git dir. No mocks: the
// -C git context fix is exactly the exec-level contract (bd-6m4).
func initChangeDirTestRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-q", "-m", "init"},
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

// isolateGitEnv clears the caller's git selection for the duration of the
// test (t.Setenv restores it afterwards) and resets the internal/git caches,
// so retargeting in one test cannot leak into another.
func isolateGitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_WORK_TREE", "")
	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("GIT_WORK_TREE"); err != nil {
		t.Fatal(err)
	}
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
}

// assertGitEnvUnset fails when retargeting left GIT_DIR or GIT_WORK_TREE set.
func assertGitEnvUnset(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE"} {
		if got, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q, want unset", key, got)
		}
	}
}

// assertSameDir fails unless got and want name the same existing directory.
// Both sides resolve through symlinks before comparing: on macOS t.TempDir
// hands out /var/folders/... while git resolves the same directory to
// /private/var/folders/..., and the contract under test is identity of the
// directory, not of its spelling.
func assertSameDir(t *testing.T, what, got, want string) {
	t.Helper()
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("%s: resolve %q: %v", what, got, err)
	}
	wantReal, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("%s: resolve %q: %v", what, want, err)
	}
	if gotReal != wantReal {
		t.Errorf("%s = %q, want %q (same directory)", what, got, want)
	}
}

func TestRetargetGitContextPointsGitEnvAtTargetRepo(t *testing.T) {
	isolateGitEnv(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	gitDirA := initChangeDirTestRepo(t, dirA)
	gitDirB := initChangeDirTestRepo(t, dirB)
	subdirB := filepath.Join(dirB, "nested", "pkg")
	if err := os.MkdirAll(subdirB, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dirA)

	// Prime the internal/git cache with the caller's repository so the test
	// observes that retargeting invalidates it. GetGitCommonDir is absolute
	// (GetGitDir echoes git's cwd-relative ".git").
	if got, err := git.GetGitCommonDir(); err != nil {
		t.Fatalf("precondition: git.GetGitCommonDir(): %v", err)
	} else {
		assertSameDir(t, "precondition: git.GetGitCommonDir()", got, gitDirA)
	}

	// Retarget from a nested directory: the repository is resolved from the
	// -C directory itself, not from where a beads directory happens to live.
	if err := retargetGitContext(subdirB); err != nil {
		t.Fatalf("retargetGitContext(%q): %v", subdirB, err)
	}
	assertSameDir(t, "GIT_DIR", os.Getenv("GIT_DIR"), gitDirB)
	wantWorkTree, err := exec.Command("git", "-C", dirB, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	assertSameDir(t, "GIT_WORK_TREE", os.Getenv("GIT_WORK_TREE"), strings.TrimSpace(string(wantWorkTree)))
	if got, err := git.GetGitCommonDir(); err != nil {
		t.Errorf("git.GetGitCommonDir() after retarget: %v", err)
	} else {
		assertSameDir(t, "git.GetGitCommonDir() after retarget", got, gitDirB)
	}
	// Decisive observable: plain git from the caller's cwd now resolves the
	// TARGET repo; this is what makes hooks install land in the -C target.
	out, err := exec.Command("git", "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	assertSameDir(t, "git rev-parse resolved", strings.TrimSpace(string(out)), gitDirB)
}

func TestRetargetGitContextNonRepoIsNoOp(t *testing.T) {
	isolateGitEnv(t)
	dir := t.TempDir() // no git repository here
	// Stop discovery at the temp dir's parent so an enclosing repository on
	// the host cannot turn this fixture into a repo.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	if err := retargetGitContext(dir); err != nil {
		t.Fatalf("retargetGitContext on non-repo must be a no-op, got: %v", err)
	}
	assertGitEnvUnset(t)
}

func TestRetargetGitContextNotAWorkTreeIsNoOp(t *testing.T) {
	isolateGitEnv(t)
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if out, err := exec.Command("git", "init", "--bare", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	if err := retargetGitContext(dir); err != nil {
		t.Fatalf("retargetGitContext on a bare repository must be a no-op, got: %v", err)
	}
	assertGitEnvUnset(t)
}

func TestRetargetGitContextPropagatesGitErrors(t *testing.T) {
	cases := []struct {
		name    string
		fixture func(t *testing.T, dir string)
		want    []string
	}{
		{
			name: "broken gitfile",
			fixture: func(t *testing.T, dir string) {
				gitfile := "gitdir: " + filepath.Join(dir, "missing") + "\n"
				if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(gitfile), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			// git's wording for a dangling gitfile changed across releases
			// ("not a git repository:" before 2.5x, "gitfile does not point to
			// a valid repo" after); the contract is that git's own failure
			// reaches the caller, so assert bd's rev-parse wrapper for this
			// directory plus git's own fatal stderr marker.
			want: []string{"rev-parse --is-inside-work-tree in -C directory", "fatal:"},
		},
		{
			name: "dubious ownership",
			fixture: func(t *testing.T, dir string) {
				initChangeDirTestRepo(t, dir)
				// git's own knob for the safe.directory ownership check. A
				// runner-wide safe.directory (CI sets "*") would bypass the
				// check, so the global and system config are kept out.
				t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
				t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
				t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
			},
			want: []string{"dubious ownership"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateGitEnv(t)
			dir := t.TempDir()
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
			tc.fixture(t, dir)

			err := retargetGitContext(dir)
			if err == nil {
				t.Fatalf("retargetGitContext(%q) = nil, want git error containing %q", dir, tc.want)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("retargetGitContext error = %q, want it to contain %q", err, want)
				}
			}
			assertGitEnvUnset(t)
		})
	}
}
