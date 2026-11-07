package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/stretchr/testify/require"
)

// linkedHooksFixture is a primary checkout P and a linked worktree W created
// after P committed a hooks directory, so both indexes track it.
type linkedHooksFixture struct {
	root, primary, worktree string
	hook                    func(name string) string // content committed in P
}

// oldMarkerHook is a bd-owned hook whose section an install must rewrite.
func oldMarkerHook(name string) string {
	return "#!/usr/bin/env sh\n" +
		strings.ReplaceAll(generateHookSection(name), " v"+Version+" ---", " v0.0.1 ---")
}

// clearGitRoutingForTest makes every inherited Git routing key absent for the
// test, so fixtures resolve the repository their working directory names.
func clearGitRoutingForTest(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			require.NoError(t, os.Unsetenv(key))
		}
	}
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BD_DISABLE_METRICS", "1")
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
}

// legacyVersionHook is a bd-owned pre-section hook whose recorded version is
// outdated, which is what makes `bd init` reinstall: init treats a
// section-marker hook as current whatever its marker version.
func legacyVersionHook(name string) string {
	return "#!/bin/sh\n# bd (beads) " + name + " hook\n" + hookVersionPrefix + "0.0.1\nexit 0\n"
}

// newLinkedHooksFixture commits old-marker hooks under rel in P when track is
// true (writing them untracked otherwise), points core.hooksPath at P's copy,
// and adds W on branch wt. The caller's cwd becomes W.
func newLinkedHooksFixture(t *testing.T, rel string, track bool) linkedHooksFixture {
	t.Helper()
	return newLinkedHooksFixtureWith(t, rel, track, oldMarkerHook)
}

func newLinkedHooksFixtureWith(t *testing.T, rel string, track bool, hook func(name string) string) linkedHooksFixture {
	t.Helper()
	clearGitRoutingForTest(t)
	// Git reports resolved paths, so a symlinked TMPDIR (macOS /var) would make
	// every path below an alias; TestInstallHooksFromLinkedWorktreeThroughPrimaryAlias
	// covers aliases on purpose.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := linkedHooksFixture{root: root, primary: filepath.Join(root, "main"), worktree: filepath.Join(root, "wt"), hook: hook}
	mkdirs(t, f.primary)
	initExcludeGit(t, f.primary, "init", "-q", "-b", "main")
	for key, value := range map[string]string{"user.name": "Fixture", "user.email": "fixture@example.test", "commit.gpgSign": "false"} {
		initExcludeGit(t, f.primary, "config", "--local", key, value)
	}
	initExcludeGit(t, f.primary, "commit", "-q", "--allow-empty", "-m", "init")
	hooksDir := filepath.Join(f.primary, rel)
	mkdirs(t, hooksDir)
	for _, name := range managedHookNames {
		require.NoError(t, os.WriteFile(filepath.Join(hooksDir, name), []byte(hook(name)), 0o755))
	}
	if track {
		initExcludeGit(t, f.primary, "add", "--", rel)
		initExcludeGit(t, f.primary, "commit", "-q", "-m", "track hooks")
	}
	initExcludeGit(t, f.primary, "config", "--local", "core.hooksPath", hooksDir)
	initExcludeGit(t, f.primary, "worktree", "add", "-q", "-b", "wt", f.worktree)
	t.Chdir(f.worktree)
	resetRepoCachesForTest(t)
	return f
}

// runHooksInstallForTest drives `bd hooks install [--mode]` and returns its
// stdout, stderr and error.
func runHooksInstallForTest(t *testing.T, mode string, asJSON bool) (string, string, error) {
	t.Helper()
	setStandaloneHookMode(t, mode)
	jsonOutput = asJSON
	var err error
	stdout, stderr := captureStdio(t, func() {
		err = hooksInstallCmd.RunE(hooksInstallCmd, nil)
	})
	return stdout, stderr, err
}

// requireHooksCurrent asserts every managed hook in dir carries the current marker.
func requireHooksCurrent(t *testing.T, dir string) {
	t.Helper()
	for _, name := range managedHookNames {
		data := readInitHooksFile(t, filepath.Join(dir, name))
		require.Contains(t, string(data), hookSectionBeginLine(), "%s in %s", name, dir)
	}
}

// requirePrimaryUnchanged asserts P has no tracked changes and its hooks under
// rel still carry the old marker.
func requirePrimaryUnchanged(t *testing.T, f linkedHooksFixture, rel string) {
	t.Helper()
	require.Empty(t, initExcludeGit(t, f.primary, "status", "--porcelain", "--untracked-files=no"),
		"install from the linked worktree dirtied the primary checkout")
	for _, name := range managedHookNames {
		data := readInitHooksFile(t, filepath.Join(f.primary, rel, name))
		require.Equal(t, f.hook(name), string(data), "primary %s changed", name)
	}
}

func requireHooksPath(t *testing.T, f linkedHooksFixture, want string) {
	t.Helper()
	require.Equal(t, want, initExcludeGit(t, f.worktree, "config", "--get", "core.hooksPath"),
		"core.hooksPath must keep naming the primary copy (#2414)")
}

// symlinkDirOrSkip creates link pointing at dir, skipping where the platform
// cannot create directory symlinks.
func symlinkDirOrSkip(t *testing.T, dir, link string) {
	t.Helper()
	if err := os.Symlink(dir, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("directory symlink capability unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

// requireSingleRedirectNote asserts stderr is exactly one note line naming
// both the written directory and the one Git runs.
func requireSingleRedirectNote(t *testing.T, stderr, written, runs string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	require.Len(t, lines, 1, "stderr: %q", stderr)
	require.Contains(t, lines[0], written)
	require.Contains(t, lines[0], runs)
}

func TestInstallHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixture(t, rel, true)

	stdout, stderr, err := runHooksInstallForTest(t, "", false)
	require.NoError(t, err)

	requireHooksCurrent(t, filepath.Join(f.worktree, rel))
	requirePrimaryUnchanged(t, f, rel)
	requireHooksPath(t, f, filepath.Join(f.primary, rel))
	requireSingleRedirectNote(t, stderr, filepath.Join(f.worktree, rel), filepath.Join(f.primary, rel))
	require.NotContains(t, stdout, "Note:")
}

func TestInstallSharedHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	rel := ".beads-hooks"
	f := newLinkedHooksFixture(t, rel, true)

	stdout, stderr, err := runHooksInstallForTest(t, "shared", false)
	require.NoError(t, err)

	requireHooksCurrent(t, filepath.Join(f.worktree, rel))
	requirePrimaryUnchanged(t, f, rel)
	requireHooksPath(t, f, filepath.Join(f.primary, rel))
	requireSingleRedirectNote(t, stderr, filepath.Join(f.worktree, rel), filepath.Join(f.primary, rel))
	require.NotContains(t, stdout, "Note:")
}

// TestInstallHooksFromLinkedWorktreeThroughPrimaryAlias pins the redirect for
// an absolute core.hooksPath that reaches the primary's copy through a
// symlink: Git reports the primary resolved, and comparing the two spellings
// lexically declined and rewrote the primary's committed hooks.
func TestInstallHooksFromLinkedWorktreeThroughPrimaryAlias(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixture(t, rel, true)
	alias := filepath.Join(t.TempDir(), "primary alias")
	symlinkDirOrSkip(t, f.primary, alias)
	aliased := filepath.Join(alias, rel)
	initExcludeGit(t, f.primary, "config", "--local", "core.hooksPath", aliased)
	resetRepoCaches()

	_, stderr, err := runHooksInstallForTest(t, "", false)
	require.NoError(t, err)

	requireHooksCurrent(t, filepath.Join(f.worktree, rel))
	requirePrimaryUnchanged(t, f, rel)
	requireHooksPath(t, f, aliased)
	requireSingleRedirectNote(t, stderr, filepath.Join(f.worktree, rel), aliased)
}

// skipWithoutGitHookRun skips where git has no `git hook run` (Git 2.36+).
// The probe names a native hook no fixture installs: newer Git refuses
// unknown hook names.
func skipWithoutGitHookRun(t *testing.T, repo string) {
	t.Helper()
	cmd := gitCommand("hook", "run", "--ignore-missing", "applypatch-msg")
	cmd.Dir, cmd.Env = repo, gitenv.ScrubRouting(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "is not a git command") {
			t.Skipf("git hook run needs Git 2.36 or later: %s", out)
		}
		t.Fatalf("git hook run probe: %v: %s", err, out)
	}
}

// requireCustomPrePushRuns asserts that Git, from every checkout, runs the
// custom pre-push hook the fixture's previous hooks directory held.
func requireCustomPrePushRuns(t *testing.T, f linkedHooksFixture) {
	t.Helper()
	for _, checkout := range []string{f.primary, f.worktree} {
		cmd := gitCommand("hook", "run", "--ignore-missing", "pre-push")
		cmd.Dir, cmd.Env = checkout, gitenv.ScrubRouting(os.Environ())
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "pre-push from %s did not fail as the custom hook does: %s", checkout, out)
		require.Equal(t, 42, exitErr.ExitCode(), "pre-push from %s: %s", checkout, out)
		require.Contains(t, string(out), "CUSTOM_VALIDATION", "pre-push from %s", checkout)
	}
}

// TestInstallSharedHooksFromLinkedWorktreeSwitchKeepsCustomHookRunning pins a
// mode switch from a worktree: --shared moves core.hooksPath off the hooks
// directory Git ran onto the primary's copy, so the install writes that copy,
// as from the primary checkout, and a custom hook preserved from the previous
// directory keeps running in every checkout. Preserving it into the
// worktree's copy left the copy Git runs without it, and the hook stopped
// running everywhere.
func TestInstallSharedHooksFromLinkedWorktreeSwitchKeepsCustomHookRunning(t *testing.T) {
	rel := ".beads-hooks"
	f := newLinkedHooksFixture(t, rel, true)
	skipWithoutGitHookRun(t, f.primary)
	prePush := filepath.ToSlash(filepath.Join(rel, "pre-push"))
	for _, checkout := range []string{f.primary, f.worktree} {
		initExcludeGit(t, checkout, "rm", "-q", "--", prePush)
		initExcludeGit(t, checkout, "commit", "-q", "-m", "drop pre-push")
	}
	previous := filepath.Join(f.root, "previous-hooks")
	mkdirs(t, previous)
	require.NoError(t, os.WriteFile(filepath.Join(previous, "pre-push"), []byte("#!/bin/sh\necho CUSTOM_VALIDATION\nexit 42\n"), 0o755))
	initExcludeGit(t, f.primary, "config", "--local", "core.hooksPath", previous)
	resetRepoCaches()
	requireCustomPrePushRuns(t, f)

	stdout, stderr, err := runHooksInstallForTest(t, "shared", false)
	require.NoError(t, err)

	require.Contains(t, stdout, "Preserving existing pre-push hook")
	requireHooksPath(t, f, filepath.Join(f.primary, rel))
	requireCustomPrePushRuns(t, f)
	require.Contains(t, string(readInitHooksFile(t, filepath.Join(f.primary, rel, "pre-push"))), "echo CUSTOM_VALIDATION")
	requireHooksCurrent(t, filepath.Join(f.primary, rel))
	require.Empty(t, stderr, "an install that moves core.hooksPath is not redirected")
	require.Empty(t, initExcludeGit(t, f.worktree, "status", "--porcelain"), "install wrote the worktree's copy, which Git does not run")
}

func TestInstallBeadsHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixture(t, rel, true)
	// An untracked embedded store makes W's discovery fall back to P's .beads.
	mkdirs(t, filepath.Join(f.primary, ".beads", "embeddeddolt"))
	resetRepoCaches()
	require.Equal(t, filepath.Join(f.primary, ".beads"), beads.FindBeadsDir(), "fixture precondition")

	stdout, stderr, err := runHooksInstallForTest(t, "beads", true)
	require.NoError(t, err)

	requireHooksCurrent(t, filepath.Join(f.worktree, rel))
	requirePrimaryUnchanged(t, f, rel)
	requireHooksPath(t, f, filepath.Join(f.primary, rel))
	requireSingleRedirectNote(t, stderr, filepath.Join(f.worktree, rel), filepath.Join(f.primary, rel))
	// The --json map is today's: no note and no directory field.
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &got), "stdout: %q", stdout)
	require.Equal(t, map[string]interface{}{
		"success": true, "message": "Git hooks installed successfully",
		"shared": false, "chained": false, "beadsHooks": true,
	}, got)
}

func TestInstallHooksFromLinkedWorktreeRefusesForeignTrackedCopy(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixture(t, rel, true)
	foreign := filepath.Join(f.worktree, rel, "pre-commit")
	require.NoError(t, os.WriteFile(foreign, []byte("#!/bin/sh\necho team hook\n"), 0o755))
	initExcludeGit(t, f.worktree, "commit", "-q", "-am", "team pre-commit")

	_, stderr, err := runHooksInstallForTest(t, "", false)
	require.Error(t, err)
	require.Contains(t, stderr, foreign+" is tracked by git")
	require.Contains(t, stderr, "not a bd-managed hook")

	requirePrimaryUnchanged(t, f, rel)
	require.Equal(t, "#!/bin/sh\necho team hook\n", string(readInitHooksFile(t, foreign)))
	for _, name := range managedHookNames[1:] {
		require.Equal(t, oldMarkerHook(name), string(readInitHooksFile(t, filepath.Join(f.worktree, rel, name))))
	}
	require.Empty(t, initExcludeGit(t, f.worktree, "status", "--porcelain"))
}

func TestInitHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixtureWith(t, rel, true, legacyVersionHook)

	captureStdio(t, func() {
		runEmbeddedInitHooks(context.Background(), f.worktree, filepath.Join(f.primary, ".beads"), false, true)
	})

	requireHooksCurrent(t, filepath.Join(f.worktree, rel))
	requirePrimaryUnchanged(t, f, rel)
	requireHooksPath(t, f, filepath.Join(f.primary, rel))
}

func TestInstallHooksFromLinkedWorktreeKeepsUntrackedPrimaryCopy(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixture(t, rel, false)

	_, stderr, err := runHooksInstallForTest(t, "", false)
	require.NoError(t, err)

	requireHooksCurrent(t, filepath.Join(f.primary, rel))
	_, statErr := os.Stat(filepath.Join(f.worktree, ".beads"))
	require.True(t, os.IsNotExist(statErr), "install created %s/.beads", f.worktree)
	require.Empty(t, stderr)
	requireHooksPath(t, f, filepath.Join(f.primary, rel))
}

func TestInstallHooksFromLinkedWorktreeIgnoresInheritedIndex(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixture(t, rel, false)
	// A decoy repository whose index does track .beads/hooks.
	decoy := filepath.Join(f.root, "decoy")
	mkdirs(t, filepath.Join(decoy, rel))
	initExcludeGit(t, decoy, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(decoy, rel, "pre-commit"), []byte(oldMarkerHook("pre-commit")), 0o755))
	initExcludeGit(t, decoy, "add", "--", rel)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	resetRepoCaches()

	_, stderr, err := runHooksInstallForTest(t, "", false)
	require.NoError(t, err)

	requireHooksCurrent(t, filepath.Join(f.primary, rel))
	_, statErr := os.Stat(filepath.Join(f.worktree, ".beads"))
	require.True(t, os.IsNotExist(statErr), "install created %s/.beads", f.worktree)
	require.NotContains(t, stderr, "Note:")
}

// writeExternalHooks creates dir, outside both checkouts, holding a custom
// pre-commit: the directory a symlinked worktree copy points the install at.
func writeExternalHooks(t *testing.T, dir string) {
	t.Helper()
	mkdirs(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pre-commit"), []byte("#!/bin/sh\necho external\n"), 0o755))
}

// TestInstallSharedHooksFromLinkedWorktreeDeclinesCommittedSymlinkCopy pins
// the redirect's containment: a worktree that commits its hooks directory as
// a symlink tracks the link, not hooks. Writing through it rewrote the custom
// hook and created bd's hooks in a directory outside the repository, while Git
// still ran the primary's copy. The install keeps the primary's copy, as it
// did before the redirect.
func TestInstallSharedHooksFromLinkedWorktreeDeclinesCommittedSymlinkCopy(t *testing.T) {
	rel := ".beads-hooks"
	f := newLinkedHooksFixture(t, rel, true)
	external := filepath.Join(f.root, "external")
	writeExternalHooks(t, external)
	initExcludeGit(t, f.worktree, "rm", "-r", "-q", "--", rel)
	symlinkDirOrSkip(t, external, filepath.Join(f.worktree, rel))
	initExcludeGit(t, f.worktree, "add", "--", rel)
	initExcludeGit(t, f.worktree, "commit", "-q", "-m", "link hooks")
	require.True(t, strings.HasPrefix(initExcludeGit(t, f.worktree, "ls-files", "--stage", "--", rel), "120000 "),
		"fixture precondition: the worktree tracks %s as a symlink", rel)
	before := snapshotTreeForDoctorTest(t, external)

	_, stderr, err := runHooksInstallForTest(t, "shared", false)
	require.NoError(t, err)

	require.Equal(t, before, snapshotTreeForDoctorTest(t, external), "install wrote outside the repository")
	requireHooksCurrent(t, filepath.Join(f.primary, rel))
	require.NotContains(t, stderr, "Note:")
	require.Empty(t, initExcludeGit(t, f.worktree, "status", "--porcelain"))
}

// TestInstallHooksFromLinkedWorktreeDeclinesSymlinkedWorkingCopy pins the same
// containment when only the working tree holds the link: the worktree's index
// still tracks .beads/hooks, but .beads on disk is a symlink to a directory
// outside the repository, so the tracked path no longer leads to the
// worktree's own files.
func TestInstallHooksFromLinkedWorktreeDeclinesSymlinkedWorkingCopy(t *testing.T) {
	rel := filepath.Join(".beads", "hooks")
	f := newLinkedHooksFixture(t, rel, true)
	external := filepath.Join(f.root, "external")
	writeExternalHooks(t, filepath.Join(external, "hooks"))
	require.NoError(t, os.RemoveAll(filepath.Join(f.worktree, ".beads")))
	symlinkDirOrSkip(t, external, filepath.Join(f.worktree, ".beads"))
	before := snapshotTreeForDoctorTest(t, external)

	_, stderr, err := runHooksInstallForTest(t, "", false)
	require.NoError(t, err)

	require.Equal(t, before, snapshotTreeForDoctorTest(t, external), "install wrote outside the repository")
	requireHooksCurrent(t, filepath.Join(f.primary, rel))
	require.NotContains(t, stderr, "Note:")
}

// TestWorktreeHooksWriteDir pins both sides of the install retarget: a linked
// worktree writes its own mirror of a hooks directory only when it and the
// primary checkout both track it; every other layout keeps the directory Git
// runs.
func TestWorktreeHooksWriteDir(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "main")
	worktree := filepath.Join(root, "wt")
	nested := filepath.Join(primary, ".worktrees", "wt")
	common := filepath.Join(primary, ".git")
	outside := filepath.Join(root, "elsewhere", "hooks")
	mkdirs(t,
		filepath.Join(primary, ".beads", "hooks"),
		filepath.Join(primary, ".beads-hooks"),
		filepath.Join(primary, "tools", "store", "hooks"),
		filepath.Join(common, "hooks"),
		filepath.Join(worktree, ".beads", "hooks"),
		nested,
		outside,
		filepath.Join(root, "repo.git"),
	)
	beadsHooks := filepath.Join(".beads", "hooks")
	linked := git.HooksContext{CommonDir: common, RepoRoot: worktree, MainRepoRoot: primary}
	both := func(roots ...string) map[[2]string]bool {
		tracks := map[[2]string]bool{}
		for _, r := range roots {
			for _, rel := range []string{beadsHooks, ".beads-hooks", filepath.Join("tools", "store", "hooks"), filepath.Join(".git", "hooks"), "."} {
				tracks[[2]string{r, rel}] = true
			}
		}
		return tracks
	}

	tests := []struct {
		name   string
		target string
		paths  git.HooksContext
		tracks map[[2]string]bool
		want   string
	}{
		{"both track .beads/hooks", filepath.Join(primary, beadsHooks), linked, both(primary, worktree), filepath.Join(worktree, beadsHooks)},
		{"both track .beads-hooks", filepath.Join(primary, ".beads-hooks"), linked, both(primary, worktree), filepath.Join(worktree, ".beads-hooks")},
		{"in-repo redirected store keeps its relative path", filepath.Join(primary, "tools", "store", "hooks"), linked, both(primary, worktree), filepath.Join(worktree, "tools", "store", "hooks")},
		{"worktree nested under the primary", filepath.Join(primary, beadsHooks),
			git.HooksContext{CommonDir: common, RepoRoot: nested, MainRepoRoot: primary}, both(primary, nested), filepath.Join(nested, beadsHooks)},
		{"only the worktree tracks", filepath.Join(primary, beadsHooks), linked, both(worktree), filepath.Join(primary, beadsHooks)},
		{"only the primary tracks", filepath.Join(primary, beadsHooks), linked, both(primary), filepath.Join(primary, beadsHooks)},
		{"neither tracks", filepath.Join(primary, beadsHooks), linked, both(), filepath.Join(primary, beadsHooks)},
		{"common dir hooks stay shared", filepath.Join(common, "hooks"), linked, both(primary, worktree), filepath.Join(common, "hooks")},
		{"target outside the primary", outside, linked, both(primary, worktree), outside},
		{"target already inside the worktree", filepath.Join(worktree, beadsHooks), linked, both(primary, worktree), filepath.Join(worktree, beadsHooks)},
		{"bare-backed common dir", filepath.Join(primary, beadsHooks),
			git.HooksContext{CommonDir: filepath.Join(root, "repo.git"), RepoRoot: worktree, MainRepoRoot: primary}, both(primary, worktree), filepath.Join(primary, beadsHooks)},
		{"empty common dir", filepath.Join(primary, beadsHooks),
			git.HooksContext{RepoRoot: worktree, MainRepoRoot: primary}, both(primary, worktree), filepath.Join(primary, beadsHooks)},
		{"install from the primary itself", filepath.Join(primary, beadsHooks),
			git.HooksContext{CommonDir: common, RepoRoot: primary, MainRepoRoot: primary}, both(primary), filepath.Join(primary, beadsHooks)},
		{"target is the primary checkout root", primary, linked, both(primary, worktree), primary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := worktreeHooksWriteDir(tt.target, tt.paths, func(root, rel string) bool {
				return tt.tracks[[2]string{root, rel}]
			})
			require.Equal(t, tt.want, got)
		})
	}
}

// TestWorktreeHooksWriteDirResolvesAliases pins the gate's comparisons on the
// resolved target: Git reports the checkouts resolved while core.hooksPath
// keeps its spelling, so an alias of the primary's copy still redirects and
// aliases of the worktree's copy or the common dir's hooks still decline.
func TestWorktreeHooksWriteDirResolvesAliases(t *testing.T) {
	root := t.TempDir()
	primary, worktree := filepath.Join(root, "main"), filepath.Join(root, "wt")
	common := filepath.Join(primary, ".git")
	beadsHooks := filepath.Join(".beads", "hooks")
	mkdirs(t, filepath.Join(primary, beadsHooks), filepath.Join(common, "hooks"), filepath.Join(worktree, beadsHooks))
	alias := func(dir string) string {
		link := filepath.Join(t.TempDir(), "alias")
		symlinkDirOrSkip(t, dir, link)
		return link
	}
	paths := git.HooksContext{CommonDir: common, RepoRoot: worktree, MainRepoRoot: primary}
	tracksAll := func(string, string) bool { return true }

	require.Equal(t, filepath.Join(worktree, beadsHooks),
		worktreeHooksWriteDir(filepath.Join(alias(primary), beadsHooks), paths, tracksAll))
	for _, target := range []string{filepath.Join(alias(worktree), beadsHooks), filepath.Join(alias(common), "hooks")} {
		require.Equal(t, target, worktreeHooksWriteDir(target, paths, tracksAll))
	}
}

// TestHooksIndexTrackerCountsOnlyFilesInsideTheDirectory pins the index half
// of the redirect gate: an index entry for rel itself, such as a committed
// symlink or a submodule, links to files this index does not hold, so it is
// not a tracked copy of the directory.
func TestHooksIndexTrackerCountsOnlyFilesInsideTheDirectory(t *testing.T) {
	clearGitRoutingForTest(t)
	repo := t.TempDir()
	initExcludeGit(t, repo, "init", "-q")
	hooks := filepath.Join(repo, ".beads", "hooks")
	mkdirs(t, hooks)
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "plain"), []byte("plain\n"), 0o644))
	initExcludeGit(t, repo, "add", "--", ".beads", "plain")
	// Index-only entries: recording a link needs no symlink capability.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "link-target"), []byte("../elsewhere"), 0o644))
	blob := initExcludeGit(t, repo, "hash-object", "-w", "--", "link-target")
	initExcludeGit(t, repo, "update-index", "--add", "--cacheinfo", "120000,"+blob+",.beads-hooks")
	initExcludeGit(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+blob+",submodule")

	tracks := hooksIndexTracker(gitenv.ScrubRouting(os.Environ()))
	for rel, want := range map[string]bool{
		filepath.Join(".beads", "hooks"): true,
		".beads-hooks":                   false, // a committed symlink
		"submodule":                      false,
		"plain":                          false,
	} {
		require.Equal(t, want, tracks(repo, rel), rel)
	}
}
