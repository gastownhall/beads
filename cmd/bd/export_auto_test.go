package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitAddFile_InWorktreeHook_StagesCorrectPath is a regression test for
// GH#3311: when bd's pre-commit hook calls git add with GIT_DIR inherited
// from the parent hook invocation, git defaults the work-tree to cwd and
// mis-stages the file at the root of the repo instead of under .beads/.
//
// This test verifies the file ends up staged at .beads/issues.jsonl, not
// at repo-root "issues.jsonl".
func TestGitAddFile_InWorktreeHook_StagesCorrectPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "bd-gh3311-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	// Resolve symlinks so toplevel comparisons below match git's canonical view
	// (on macOS /var -> /private/var).
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	mainRepo := filepath.Join(tmpDir, "main")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
		}
	}
	runGit(mainRepo, "init", "-q")
	runGit(mainRepo, "config", "user.email", "t@t")
	runGit(mainRepo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "add", "README.md")
	runGit(mainRepo, "commit", "-qm", "init")

	worktree := filepath.Join(tmpDir, "wt")
	runGit(mainRepo, "worktree", "add", worktree, "-b", "feat")
	t.Cleanup(func() {
		c := exec.Command("git", "worktree", "remove", "--force", worktree)
		c.Dir = mainRepo
		_ = c.Run()
	})

	beadsDir := filepath.Join(worktree, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate the environment inside a git pre-commit hook: GIT_DIR points
	// at the worktree's per-worktree gitdir.
	out, err := exec.Command("git", "-C", worktree, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	if gitDir, err = filepath.EvalSymlinks(gitDir); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_DIR", gitDir)

	// Call the function under test from a state that matches the hook
	// subprocess: cwd not particularly interesting here, but gitAddFile sets
	// cmd.Dir = filepath.Dir(path) internally.
	t.Chdir(worktree)
	if err := gitAddFile(jsonlPath); err != nil {
		t.Fatalf("gitAddFile: %v", err)
	}

	// Inspect the worktree's index: the staged path must be ".beads/issues.jsonl",
	// NOT bare "issues.jsonl" at repo root.
	lsFiles := exec.Command("git", "ls-files", "--stage")
	lsFiles.Dir = worktree
	data, err := lsFiles.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files: %v\n%s", err, data)
	}
	staged := string(data)
	if !strings.Contains(staged, ".beads/issues.jsonl") {
		t.Errorf("expected .beads/issues.jsonl to be staged, got:\n%s", staged)
	}
	// Regression guard: the pre-fix bug stages bare "issues.jsonl" at the root.
	for _, line := range strings.Split(strings.TrimSpace(staged), "\n") {
		// Each line is "<mode> <sha> <stage>\t<path>"
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[1] == "issues.jsonl" {
			t.Errorf("regression: issues.jsonl staged at repo root (GH#3311):\n%s", staged)
		}
	}
}

// TestScrubGitHookEnv verifies that the env-scrubbing helper drops exactly
// the git-hook-injected variables that would otherwise poison `git add`'s
// repo auto-discovery (or divert its object writes / config).
func TestScrubGitHookEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GIT_DIR=/some/.git",
		"GIT_WORK_TREE=/some",
		"GIT_INDEX_FILE=/some/.git/index",
		"GIT_COMMON_DIR=/some/.git",
		"GIT_PREFIX=sub/",
		"GIT_OBJECT_DIRECTORY=/some/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/elsewhere/.git/objects",
		"GIT_CEILING_DIRECTORIES=/home",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
		"GIT_CONFIG=/etc/some.conf",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree",
		"GIT_CONFIG_VALUE_0=/elsewhere",
		"GIT_CONFIG_PARAMETERS='core.worktree=/elsewhere'",
		"GIT_CONFIG_GLOBAL=/tmp/gcfg",
		"GIT_CONFIG_SYSTEM=/tmp/scfg",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME=/home/u",
		// Non-discovery vars that must pass through.
		"GIT_AUTHOR_NAME=kept",
		"GIT_COMMITTER_EMAIL=kept@example.com",
		"GIT_EDITOR=vim",
		"GIT_PAGER=less",
	}
	out := scrubGitHookEnv(in)
	joined := strings.Join(out, "\n")
	banned := []string{
		"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
		"GIT_PREFIX=", "GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=",
		"GIT_CEILING_DIRECTORIES=", "GIT_DISCOVERY_ACROSS_FILESYSTEM=",
		"GIT_CONFIG=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_0=", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_PARAMETERS=", "GIT_CONFIG_GLOBAL=", "GIT_CONFIG_SYSTEM=", "GIT_CONFIG_NOSYSTEM=",
	}
	for _, b := range banned {
		if strings.Contains(joined, b) {
			t.Errorf("scrubGitHookEnv leaked %s\nresult:\n%s", b, joined)
		}
	}
	kept := []string{
		"PATH=/usr/bin", "HOME=/home/u",
		"GIT_AUTHOR_NAME=kept", "GIT_COMMITTER_EMAIL=kept@example.com",
		"GIT_EDITOR=vim", "GIT_PAGER=less",
	}
	for _, k := range kept {
		if !strings.Contains(joined, k) {
			t.Errorf("scrubGitHookEnv dropped %s\nresult:\n%s", k, joined)
		}
	}
}

// TestPathInsideDir covers the common structural cases plus the
// fresh-file + symlinked-parent case that tripped the initial fix
// (macOS /tmp -> /private/tmp asymmetry when the target file doesn't
// yet exist).
func TestPathInsideDir(t *testing.T) {
	tmpRaw, err := os.MkdirTemp("", "bd-pathinside-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpRaw) })

	// Provoke a symlinked-parent asymmetry: keep `raw` as the un-resolved
	// tmp form (/tmp/...) and derive `real` as the canonical form
	// (/private/tmp/...) so tests can compare across the boundary.
	real, err := filepath.EvalSymlinks(tmpRaw)
	if err != nil {
		t.Fatal(err)
	}

	wt := filepath.Join(real, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	wtRaw := filepath.Join(tmpRaw, "wt") // un-resolved view of same dir

	existing := filepath.Join(wt, "existing.txt")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		dir  string
		want bool
	}{
		{"identical paths", wt, wt, true},
		{"existing descendant", existing, wt, true},
		{"fresh nonexistent descendant", filepath.Join(wt, "not-yet.txt"), wt, true},
		{"sibling path with shared prefix", filepath.Join(real, "wt-other/x"), wt, false},
		{"outside dir", filepath.Join(real, "elsewhere/x"), wt, false},
		// The regression: fresh path expressed via /tmp symlink vs dir
		// expressed via /private/tmp canonical. Must still say "inside".
		{"fresh path with symlinked parent form", filepath.Join(wtRaw, "fresh.txt"), wt, true},
		{"existing path with symlinked parent form", filepath.Join(wtRaw, "existing.txt"), wt, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathInsideDir(tc.path, tc.dir)
			if got != tc.want {
				t.Errorf("pathInsideDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
			}
		})
	}
}

// TestHookWorkTreeRoot covers the documented GIT_DIR shapes and the
// not-a-hook case.
func TestHookWorkTreeRoot(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-hwt-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Case 1: GIT_DIR not set → "" (normal non-hook context).
	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	if got := hookWorkTreeRoot(); got != "" {
		t.Errorf("with GIT_DIR unset: hookWorkTreeRoot = %q, want \"\"", got)
	}

	// Case 2: linked-worktree style — GIT_DIR = main/.git/worktrees/<n>,
	// and that dir contains a `gitdir` file pointing at the worktree's
	// .git file. Worktree root = parent of that .git file.
	wtDotGit := filepath.Join(tmpDir, "wt", ".git")
	if err := os.MkdirAll(filepath.Dir(wtDotGit), 0o755); err != nil {
		t.Fatal(err)
	}
	linkedGitDir := filepath.Join(tmpDir, "main", ".git", "worktrees", "wt")
	if err := os.MkdirAll(linkedGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkedGitDir, "gitdir"), []byte(wtDotGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", linkedGitDir)
	if got, want := hookWorkTreeRoot(), filepath.Dir(wtDotGit); got != want {
		t.Errorf("linked worktree: hookWorkTreeRoot = %q, want %q", got, want)
	}

	// Case 3: plain repo — GIT_DIR = <repo>/.git. Worktree root is its parent.
	plainGitDir := filepath.Join(tmpDir, "plain", ".git")
	if err := os.MkdirAll(plainGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", plainGitDir)
	if got, want := hookWorkTreeRoot(), filepath.Dir(plainGitDir); got != want {
		t.Errorf("plain repo: hookWorkTreeRoot = %q, want %q", got, want)
	}

	// Case 4: unrecognized shape (no gitdir file, basename != .git) → "".
	// Bare-repo-ish; we conservatively decline to identify a worktree.
	bare := filepath.Join(tmpDir, "bare.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", bare)
	if got := hookWorkTreeRoot(); got != "" {
		t.Errorf("bare/unrecognized GIT_DIR: hookWorkTreeRoot = %q, want \"\"", got)
	}
}

// TestGitAddFile_NonHookContext_GuardDoesNotFire verifies the worktree
// guard is a no-op when GIT_DIR is not set (normal bd invocation, not
// inside a git hook). Regression guard so a future tightening of
// hookWorkTreeRoot does not silently break the common path.
func TestGitAddFile_NonHookContext_GuardDoesNotFire(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "bd-nonhook-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "t@t")
	runGit("config", "user.name", "t")

	target := filepath.Join(repo, ".beads", "issues.jsonl")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"id":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if err := gitAddFile(target); err != nil {
		t.Fatalf("gitAddFile: %v", err)
	}

	c := exec.Command("git", "ls-files", "--stage")
	c.Dir = repo
	data, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-files: %v\n%s", err, data)
	}
	if !strings.Contains(string(data), ".beads/issues.jsonl") {
		t.Errorf("non-hook path did not stage .beads/issues.jsonl:\n%s", data)
	}
}

// TestGitAddFile_RedirectCase_DoesNotStageInMainRepo regresses the
// silent-stage-in-main follow-up from the GH#3311 review: when a worktree
// has .beads/redirect -> main/.beads, the worktree's pre-commit hook must
// NOT stage the redirected path into main's index. That would silently
// pollute a repo the user did not tell us to touch. Expected behavior is
// to skip staging entirely (the file content on disk is still correct).
func TestGitAddFile_RedirectCase_DoesNotStageInMainRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "bd-gh3311-redirect-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	mainRepo := filepath.Join(tmpDir, "main")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
		}
	}
	runGit(mainRepo, "init", "-q")
	runGit(mainRepo, "config", "user.email", "t@t")
	runGit(mainRepo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "add", "README.md")
	runGit(mainRepo, "commit", "-qm", "init")

	// Create main's .beads directory with an issues.jsonl the hook would
	// target via the redirect.
	mainBeads := filepath.Join(mainRepo, ".beads")
	if err := os.MkdirAll(mainBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	mainJSONL := filepath.Join(mainBeads, "issues.jsonl")
	if err := os.WriteFile(mainJSONL, []byte(`{"id":"from-redirect"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create worktree; GIT_DIR env var simulation captures the hook context.
	worktree := filepath.Join(tmpDir, "wt")
	runGit(mainRepo, "worktree", "add", worktree, "-b", "feat")
	t.Cleanup(func() {
		c := exec.Command("git", "worktree", "remove", "--force", worktree)
		c.Dir = mainRepo
		_ = c.Run()
	})

	out, err := exec.Command("git", "-C", worktree, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	if gitDir, err = filepath.EvalSymlinks(gitDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", gitDir)

	// Act: stage the main-repo-resident path from inside the worktree hook.
	t.Chdir(worktree)
	if err := gitAddFile(mainJSONL); err != nil {
		t.Fatalf("gitAddFile: %v", err)
	}

	// Assert: neither the worktree's index nor main's index got a bogus
	// staging entry from the worktree's hook firing.
	checkNoStage := func(label, repoDir string) {
		t.Helper()
		c := exec.Command("git", "ls-files", "--stage")
		c.Dir = repoDir
		data, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: ls-files: %v\n%s", label, err, data)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) != 2 {
				continue
			}
			if strings.Contains(parts[1], "issues.jsonl") {
				t.Errorf("%s staged issues.jsonl when it should not have; ls-files output:\n%s", label, data)
			}
		}
	}
	// Both checks use env with GIT_DIR unset so we observe each repo's
	// own index rather than routing through the inherited hook gitdir.
	// t.Setenv can only set (not unset); the outer Setenv of GIT_DIR has
	// a Cleanup that restores it, so unsetting here is safe for the rest
	// of this test and the outer cleanup will re-set if another test
	// relies on the parent env.
	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	checkNoStage("worktree", worktree)
	checkNoStage("main", mainRepo)
}
