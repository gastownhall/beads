//go:build linux

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// discoveryFixtureEnv is the environment fixture git runs under: the process
// environment minus every discovery override, with no system or user config.
func discoveryFixtureEnv(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") || name == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
}

func runFixtureGit(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// gitRevParse is the reference answer discoverGitFrom must reproduce.
func gitRevParse(t *testing.T, env []string, dir string) revParseResult {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--git-dir", "--git-common-dir", "--show-toplevel")
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.Output()
	if err != nil {
		return revParseResult{notRepo: true}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		t.Fatalf("git rev-parse in %s: unexpected output %q", dir, out)
	}
	return revParseResult{gitDir: lines[0], commonDir: lines[1], topLevel: lines[2]}
}

type discoveryFixture struct {
	root, main, worktree, relWorktree, symWorktree, submodule, nested string
	env                                                               []string
}

func newDiscoveryFixture(t *testing.T) discoveryFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := discoveryFixtureEnv(t)
	f := discoveryFixture{
		root:        root,
		main:        filepath.Join(root, "main"),
		worktree:    filepath.Join(root, "wt"),
		relWorktree: filepath.Join(root, "wt-rel"),
		symWorktree: filepath.Join(root, "wt-sym"),
		env:         env,
	}
	f.submodule = filepath.Join(f.main, "sub")
	f.nested = filepath.Join(f.main, "a", "inner")
	subSrc := filepath.Join(root, "subsrc")
	for _, dir := range []string{f.main, subSrc, filepath.Join(f.main, "a", "b"), f.nested, filepath.Join(root, "norepo", "x")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{f.main, subSrc} {
		runFixtureGit(t, env, dir, "init", "-q")
		runFixtureGit(t, env, dir, "commit", "-q", "--allow-empty", "-m", "base")
	}
	runFixtureGit(t, env, f.main, "worktree", "add", "-q", f.worktree)
	runFixtureGit(t, env, f.main, "worktree", "add", "-q", f.relWorktree)
	// A relative gitfile, as `git worktree add --relative-paths` (or a moved
	// checkout) writes it.
	if err := os.WriteFile(filepath.Join(f.relWorktree, ".git"), []byte("gitdir: ../main/.git/worktrees/wt-rel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A gitfile naming its git directory through a symlink: Git prints the
	// resolved path.
	runFixtureGit(t, env, f.main, "worktree", "add", "-q", f.symWorktree)
	if err := os.Symlink(filepath.Join(f.main, ".git"), filepath.Join(root, "gitlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.symWorktree, ".git"), []byte("gitdir: "+filepath.Join(root, "gitlink", "worktrees", "wt-sym")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, env, f.main, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	runFixtureGit(t, env, f.nested, "init", "-q")
	for _, dir := range []string{filepath.Join(f.worktree, "w"), filepath.Join(f.submodule, "s"), filepath.Join(f.nested, "deep")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(f.main, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestDiscoverGitFromMatchesRevParse is the equivalence contract: wherever
// in-process discovery answers, its answer is byte-for-byte what `git
// rev-parse --git-dir --git-common-dir --show-toplevel` prints there.
func TestDiscoverGitFromMatchesRevParse(t *testing.T) {
	f := newDiscoveryFixture(t)
	for _, tc := range []struct{ name, dir string }{
		{"repo top", f.main},
		{"repo subdir", filepath.Join(f.main, "a", "b")},
		{"repo first-level subdir", filepath.Join(f.main, "a")},
		{"linked worktree top", f.worktree},
		{"linked worktree subdir", filepath.Join(f.worktree, "w")},
		{"linked worktree with relative gitfile", f.relWorktree},
		{"linked worktree with gitfile through a symlink", f.symWorktree},
		{"submodule top", f.submodule},
		{"submodule subdir", filepath.Join(f.submodule, "s")},
		{"nested repo top", f.nested},
		{"nested repo subdir", filepath.Join(f.nested, "deep")},
		{"symlinked path to repo top", filepath.Join(f.root, "link")},
		{"symlinked path to repo subdir", filepath.Join(f.root, "link", "a", "b")},
		{"no repository", filepath.Join(f.root, "norepo", "x")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := discoverGitFrom(tc.dir)
			if !ok {
				if tc.name == "no repository" {
					// An ancestor of the temp dir may be unusual (a bare
					// repository, a foreign owner); declining is always safe.
					t.Skip("in-process discovery declined above the fixture root")
				}
				t.Fatalf("discoverGitFrom(%s) declined; want an in-process answer", tc.dir)
			}
			if want := gitRevParse(t, f.env, tc.dir); got != want {
				t.Fatalf("discoverGitFrom(%s) = %+v, git rev-parse = %+v", tc.dir, got, want)
			}
		})
	}
}

// TestDiscoverGitFromDeclinesUnmodeledLayouts covers the layouts in-process
// discovery must hand to git rather than guess.
func TestDiscoverGitFromDeclinesUnmodeledLayouts(t *testing.T) {
	f := newDiscoveryFixture(t)
	repoWithConfig := func(t *testing.T, args ...string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "r")
		runFixtureGit(t, f.env, t.TempDir(), "init", "-q", dir)
		runFixtureGit(t, f.env, dir, append([]string{"config"}, args...)...)
		return dir
	}
	invalidGitfile := filepath.Join(t.TempDir(), "badfile")
	if err := os.MkdirAll(invalidGitfile, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalidGitfile, ".git"), []byte("not a gitfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkedDotGit := filepath.Join(t.TempDir(), "symlinked")
	if err := os.MkdirAll(symlinkedDotGit, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.main, ".git"), filepath.Join(symlinkedDotGit, ".git")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, dir string }{
		{"core.worktree", repoWithConfig(t, "core.worktree", t.TempDir())},
		{"core.bare", repoWithConfig(t, "core.bare", "true")},
		{"extensions.worktreeConfig", repoWithConfig(t, "extensions.worktreeConfig", "true")},
		{"include directive", repoWithConfig(t, "include.path", "/dev/null")},
		{"inside the git directory", filepath.Join(f.main, ".git", "refs")},
		{"invalid gitfile", invalidGitfile},
		{"symlinked .git", symlinkedDotGit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := discoverGitFrom(tc.dir); ok {
				t.Fatalf("discoverGitFrom(%s) = %+v; want it to defer to git", tc.dir, got)
			}
		})
	}
}

// TestDiscoverGitInProcessDefersToGitEnvOverrides: GIT_DIR and friends
// redefine discovery, so their presence (even empty) sends it to git.
func TestDiscoverGitInProcessDefersToGitEnvOverrides(t *testing.T) {
	f := newDiscoveryFixture(t)
	t.Chdir(f.main)
	for _, name := range discoveryEnvOverrides {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "")
			if got, ok := discoverGitInProcess(); ok {
				t.Fatalf("with %s set, discoverGitInProcess() = %+v; want it to defer to git", name, got)
			}
		})
	}
}

// TestGitContextInProcessMatchesGitSubprocess checks the cached context end
// to end: what bd's startup now derives without git equals what it derived
// from the git subprocess, for each discovery layout.
func TestGitContextInProcessMatchesGitSubprocess(t *testing.T) {
	f := newDiscoveryFixture(t)
	for _, k := range discoveryEnvOverrides {
		if _, set := os.LookupEnv(k); set {
			t.Setenv(k, "")
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, dir := range []string{
		f.main, filepath.Join(f.main, "a", "b"), f.worktree, filepath.Join(f.worktree, "w"),
		f.submodule, filepath.Join(f.nested, "deep"),
	} {
		t.Run(strings.TrimPrefix(dir, f.root), func(t *testing.T) {
			t.Chdir(dir)
			raw, ok := discoverGitInProcess()
			if !ok {
				t.Fatalf("discoverGitInProcess declined in %s", dir)
			}
			got := gitContextFromRevParse("", raw)
			want := loadGitContext("", f.env)
			if got.err != nil || want.err != nil {
				t.Fatalf("errors: in-process %v, git %v", got.err, want.err)
			}
			if got.gitDirRaw != want.gitDirRaw || got.commonDir != want.commonDir ||
				got.repoRoot != want.repoRoot || got.isWorktree != want.isWorktree {
				t.Fatalf("in-process %+v, git %+v", got, want)
			}
		})
	}
}
