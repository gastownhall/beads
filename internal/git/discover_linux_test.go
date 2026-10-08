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

// resolvedLikeStartup is what bd's startup derives for dir: the in-process
// answer when discovery gives one, else git's.
func resolvedLikeStartup(dir string, env []string) (gitContext, bool) {
	if raw, ok := discoverGitFrom(dir); ok {
		return gitContextFromRevParse(dir, raw), true
	}
	return loadGitContext(dir, env), false
}

// assertSameAsGit checks the startup-derived context for dir against the git
// subprocess's: same failure, or the same paths.
func assertSameAsGit(t *testing.T, env []string, dir string) {
	t.Helper()
	got, _ := resolvedLikeStartup(dir, env)
	want := loadGitContext(dir, env)
	if (got.err != nil) != (want.err != nil) {
		t.Fatalf("in %s: startup err %v, git err %v", dir, got.err, want.err)
	}
	if got.gitDirRaw != want.gitDirRaw || got.commonDir != want.commonDir ||
		got.repoRoot != want.repoRoot || got.isWorktree != want.isWorktree {
		t.Fatalf("in %s: startup %+v, git %+v", dir, got, want)
	}
}

// repoWithRawConfig makes a repository and replaces its config file.
func repoWithRawConfig(t *testing.T, env []string, config string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "r")
	runFixtureGit(t, env, t.TempDir(), "init", "-q", dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const plainRepoConfig = "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n"

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
		// No ancestor of a fresh temp directory is a repository, so this is
		// answered in-process (by the walk reaching / or a mount boundary).
		{"no repository", filepath.Join(f.root, "norepo", "x")},
		{"config with a UTF-8 BOM", repoWithRawConfig(t, f.env, "\xef\xbb\xbf"+plainRepoConfig)},
		{"config with CRLF and comments", repoWithRawConfig(t, f.env,
			"# leading comment\r\n[core] ; header comment\r\n\tbare = false # trailing\r\n[remote \"origin\"]\r\n\turl = \"/x y\"\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := discoverGitFrom(tc.dir)
			if !ok {
				t.Fatalf("discoverGitFrom(%s) declined; want an in-process answer", tc.dir)
			}
			if want := gitRevParse(t, f.env, tc.dir); got != want {
				t.Fatalf("discoverGitFrom(%s) = %+v, git rev-parse = %+v", tc.dir, got, want)
			}
			assertSameAsGit(t, f.env, tc.dir)
		})
	}
}

// TestDiscoverGitFromDeclinesUnmodeledLayouts covers the layouts in-process
// discovery must hand to git rather than guess, and checks that what startup
// then derives (from git) is git's answer.
func TestDiscoverGitFromDeclinesUnmodeledLayouts(t *testing.T) {
	f := newDiscoveryFixture(t)
	repoWithConfig := func(t *testing.T, args ...string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "r")
		runFixtureGit(t, f.env, t.TempDir(), "init", "-q", dir)
		runFixtureGit(t, f.env, dir, append([]string{"config"}, args...)...)
		return dir
	}
	mkdir := func(t *testing.T, parts ...string) string {
		t.Helper()
		dir := filepath.Join(parts...)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	invalidGitfile := mkdir(t, t.TempDir(), "badfile")
	if err := os.WriteFile(filepath.Join(invalidGitfile, ".git"), []byte("not a gitfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkedDotGit := mkdir(t, t.TempDir(), "symlinked")
	if err := os.Symlink(filepath.Join(f.main, ".git"), filepath.Join(symlinkedDotGit, ".git")); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "bare.git")
	runFixtureGit(t, f.env, t.TempDir(), "init", "-q", "--bare", bare)
	bareSub := mkdir(t, bare, "sub") // below a bare repository: the walk meets HEAD+objects
	commondirInDotGit := repoWithConfig(t, "core.filemode", "true")
	if err := os.WriteFile(filepath.Join(commondirInDotGit, ".git", "commondir"), []byte(".\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, dir string }{
		{"core.worktree", repoWithConfig(t, "core.worktree", t.TempDir())},
		{"core.bare", repoWithConfig(t, "core.bare", "true")},
		{"extensions.worktreeConfig", repoWithConfig(t, "extensions.worktreeConfig", "true")},
		{"include directive", repoWithConfig(t, "include.path", "/dev/null")},
		// Git skips a leading BOM and honors the section after it.
		{"BOM then core.worktree", repoWithRawConfig(t, f.env, "\xef\xbb\xbf[core]\n\tworktree = "+t.TempDir()+"\n")},
		{"header with inner spaces", repoWithRawConfig(t, f.env, "[ core ]\n\tbare = false\n")},
		{"header with leading whitespace", repoWithRawConfig(t, f.env, "  [core]\n\tbare = false\n")},
		{"malformed key line", repoWithRawConfig(t, f.env, plainRepoConfig+"!!! = x\n")},
		{"key before any section", repoWithRawConfig(t, f.env, "bare = false\n"+plainRepoConfig)},
		{"key on the header line", repoWithRawConfig(t, f.env, "[core] bare = false\n")},
		{"escaped value", repoWithRawConfig(t, f.env, plainRepoConfig+"[alias]\n\tx = \"a\\tb\"\n")},
		{"unterminated quote", repoWithRawConfig(t, f.env, plainRepoConfig+"[alias]\n\tx = \"a\n")},
		{"continuation line", repoWithRawConfig(t, f.env, plainRepoConfig+"[alias]\n\tx = a \\\n b\n")},
		{"dotted section", repoWithRawConfig(t, f.env, "[core.x]\n\ty = 1\n"+plainRepoConfig)},
		{"inside the git directory", filepath.Join(f.main, ".git", "refs")},
		{"bare repository", bare},
		{"below a bare repository", bareSub},
		{"commondir inside .git", commondirInDotGit},
		{"invalid gitfile", invalidGitfile},
		{"symlinked .git", symlinkedDotGit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := discoverGitFrom(tc.dir); ok {
				t.Fatalf("discoverGitFrom(%s) = %+v; want it to defer to git", tc.dir, got)
			}
			assertSameAsGit(t, f.env, tc.dir)
		})
	}
}

// TestDiscoverGitInProcessUsesPhysicalCwd: Git walks from getcwd(2); a $PWD
// naming the same directory through another path (a symlink here; a bind
// mount alias in containers) must not change the walk.
func TestDiscoverGitInProcessUsesPhysicalCwd(t *testing.T) {
	f := newDiscoveryFixture(t)
	physical := filepath.Join(f.main, "a", "b")
	alias := filepath.Join(f.root, "link", "a", "b")
	t.Chdir(physical)
	t.Setenv("PWD", alias)
	if wd, err := os.Getwd(); err != nil || wd != alias {
		t.Fatalf("os.Getwd() = %q, %v; this test needs it to report the $PWD alias %q", wd, err, alias)
	}
	if wd, err := processCwd(); err != nil || wd != physical {
		t.Fatalf("processCwd() = %q, %v; want getcwd's %q", wd, err, physical)
	}
	for _, k := range discoveryEnvOverrides {
		if _, set := os.LookupEnv(k); set {
			t.Setenv(k, "")
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, ok := discoverGitInProcess()
	if !ok {
		t.Fatal("discoverGitInProcess declined")
	}
	if want := gitRevParse(t, f.env, physical); got != want {
		t.Fatalf("discoverGitInProcess() = %+v, git rev-parse = %+v", got, want)
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

// isolateGitConfigEnv points git and the in-process readers at the same,
// controlled config files: no system config, an empty global file.
func isolateGitConfigEnv(t *testing.T) (global string) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	global = filepath.Join(home, "global.gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	return global
}

func gitRemoteListsAny(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("git", "remote")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// TestHasRemoteInProcessMatchesGitRemote: wherever HasRemoteInProcess
// answers, it is what `git remote` (the auto-backup probe) reports.
func TestHasRemoteInProcessMatchesGitRemote(t *testing.T) {
	f := newDiscoveryFixture(t)
	global := isolateGitConfigEnv(t)
	withRemote := filepath.Join(t.TempDir(), "with-remote")
	runFixtureGit(t, f.env, t.TempDir(), "init", "-q", withRemote)
	runFixtureGit(t, f.env, withRemote, "remote", "add", "origin", "https://example.invalid/r.git")
	pushDefaultOnly := filepath.Join(t.TempDir(), "push-default")
	runFixtureGit(t, f.env, t.TempDir(), "init", "-q", pushDefaultOnly)
	runFixtureGit(t, f.env, pushDefaultOnly, "config", "remote.pushDefault", "origin")
	runFixtureGit(t, f.env, f.main, "remote", "add", "up", "https://example.invalid/m.git")

	setGlobal := func(t *testing.T, content string) {
		t.Helper()
		if err := os.WriteFile(global, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(global, nil, 0o600) })
	}
	for _, tc := range []struct {
		name, dir, global string
		wantDecline       bool
	}{
		{name: "no remote", dir: f.nested},
		{name: "local remote", dir: withRemote},
		{name: "remote.pushDefault only", dir: pushDefaultOnly},
		{name: "linked worktree sees the common config", dir: filepath.Join(f.worktree, "w")},
		{name: "submodule (origin in its own config)"},
		{name: "no repository", dir: filepath.Join(f.root, "norepo", "x")},
		{name: "remote in global config", dir: f.nested, global: "[remote \"g\"]\n\turl = /g\n"},
		{name: "include in global config", dir: f.nested, global: "[include]\n\tpath = /dev/null\n", wantDecline: true},
		{name: "malformed global config", dir: f.nested, global: "[ user ]\n", wantDecline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir
			if dir == "" {
				dir = f.submodule
			}
			if tc.global != "" {
				setGlobal(t, tc.global)
			}
			has, ok := HasRemoteInProcess(dir)
			if ok == tc.wantDecline {
				t.Fatalf("HasRemoteInProcess(%s) ok=%v, want ok=%v", dir, ok, !tc.wantDecline)
			}
			if want := gitRemoteListsAny(t, dir); ok && has != want {
				t.Fatalf("HasRemoteInProcess(%s) = %v, git remote lists any = %v", dir, has, want)
			}
		})
	}
	t.Run("GIT_CONFIG_PARAMETERS declines", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_PARAMETERS", "'remote.x.url'='/x'")
		if _, ok := HasRemoteInProcess(f.nested); ok {
			t.Fatal("HasRemoteInProcess answered despite command-line config")
		}
	})
	t.Run("process cwd", func(t *testing.T) {
		t.Chdir(withRemote)
		if has, ok := HasRemoteInProcess(""); !ok || !has {
			t.Fatalf("HasRemoteInProcess(\"\") = %v, %v; want true, true", has, ok)
		}
	})
}

// TestCommonDirInProcessMatchesGit: the auto-backup RepoContext asks
// `git -C <.beads> rev-parse --git-common-dir`; the in-process answer has
// git's spelling.
func TestCommonDirInProcessMatchesGit(t *testing.T) {
	f := newDiscoveryFixture(t)
	isolateGitConfigEnv(t)
	beadsDir := filepath.Join(f.main, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		f.main, beadsDir, filepath.Join(f.main, "a", "b"), f.worktree, f.submodule,
		filepath.Join(f.root, "link", "a"), filepath.Join(f.root, "norepo", "x"),
	} {
		t.Run(strings.TrimPrefix(dir, f.root), func(t *testing.T) {
			got, isRepo, ok := CommonDirInProcess(dir)
			if !ok {
				t.Fatalf("CommonDirInProcess(%s) declined", dir)
			}
			cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
			out, err := cmd.Output()
			if (err == nil) != isRepo {
				t.Fatalf("CommonDirInProcess(%s) isRepo=%v, git err=%v", dir, isRepo, err)
			}
			if want := strings.TrimSpace(string(out)); isRepo && got != want {
				t.Fatalf("CommonDirInProcess(%s) = %q, git = %q", dir, got, want)
			}
		})
	}
}
