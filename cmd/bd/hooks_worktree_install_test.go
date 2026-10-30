package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/stretchr/testify/require"
)

// writeOldMarkerHook writes the hook as an earlier bd release would have left
// it: this build's section with both marker lines aged to v0.0.1, so a
// reinstall must rewrite every managed hook.
func writeOldMarkerHook(t *testing.T, dir, name string) {
	t.Helper()
	body := "#!/usr/bin/env sh\n" + generateHookSection(name)
	body = strings.ReplaceAll(body, " v"+Version+" ---", " v0.0.1 ---")
	path := filepath.Join(dir, name)
	writeFileForWorktreeTest(t, path, []byte(body))
	require.NoError(t, os.Chmod(path, 0o755))
}

// newWorktreeHooksFixture builds the PR #7725 shape: a primary checkout P
// whose index tracks hookDirRel (a path relative to P, e.g. ".beads/hooks" or
// ".beads-hooks") holding every managed hook with the aged marker, an absolute
// core.hooksPath naming P's copy, and a linked worktree W created afterwards
// so both indexes track the same files. When trackHooks is false the hook
// directory is left untracked in P and W is created without it.
func newWorktreeHooksFixture(t *testing.T, hookDirRel string, trackHooks bool) (primary, worktree string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	primary = filepath.Join(root, "main")
	mkdirs(t, primary)
	runGitForWorktreeTest(t, primary, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, primary, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, primary, "config", "user.name", "test")
	runGitForWorktreeTest(t, primary, "commit", "-q", "--allow-empty", "-m", "init")
	hooksDir := filepath.Join(primary, hookDirRel)
	mkdirs(t, hooksDir)
	for _, name := range managedHookNames {
		writeOldMarkerHook(t, hooksDir, name)
	}
	if trackHooks {
		runGitForWorktreeTest(t, primary, "add", hookDirRel)
		runGitForWorktreeTest(t, primary, "commit", "-q", "-m", "track hooks")
	}
	runGitForWorktreeTest(t, primary, "config", "core.hooksPath", hooksDir)
	worktree = filepath.Join(root, "wt")
	runGitForWorktreeTest(t, primary, "worktree", "add", "-q", worktree, "-b", "wt")
	return primary, worktree
}

// enterWorktreeForInstallTest moves the test into the linked worktree with
// fresh repository discovery and no explicit BEADS_DIR — the shell state an
// install run sees.
func enterWorktreeForInstallTest(t *testing.T, worktree string) {
	t.Helper()
	t.Chdir(worktree)
	resetRepoCachesForTest(t)
	t.Setenv("BEADS_DIR", "")
}

// requirePrimaryUnchanged pins "P is unchanged": no tracked path in P is
// modified and P's hooks still carry the aged marker.
func requirePrimaryUnchanged(t *testing.T, primary, hookDirRel string) {
	t.Helper()
	status := initExcludeGit(t, primary, "status", "--porcelain", "--untracked-files=no")
	require.Empty(t, status, "install from the linked worktree dirtied the primary checkout")
	requireHooksCarryMarker(t, filepath.Join(primary, hookDirRel), "v0.0.1")
}

// requireHooksCurrent asserts every managed hook in dir carries the marker
// line this build generates and no longer carries the aged one.
func requireHooksCurrent(t *testing.T, dir string) {
	t.Helper()
	requireHooksCarryMarker(t, dir, hookSectionBeginLine())
	for _, name := range managedHookNames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, "read %s", name)
		require.NotContains(t, string(data), "v0.0.1", "%s in %s still carries the aged marker", name, dir)
	}
}

func requireHooksCarryMarker(t *testing.T, dir, marker string) {
	t.Helper()
	for _, name := range managedHookNames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, "read %s in %s", name, dir)
		require.Contains(t, string(data), marker, "%s in %s", name, dir)
	}
}

// requireRedirectNote pins AC-4: stderr carries exactly one note line naming
// both the directory the install wrote and the directory Git runs.
func requireRedirectNote(t *testing.T, stderr, written, runs string) {
	t.Helper()
	require.Equal(t, 1, strings.Count(stderr, "Note:"), "stderr must carry exactly one note line: %q", stderr)
	require.Contains(t, stderr, written, "the note must name the directory written")
	require.Contains(t, stderr, runs, "the note must name the directory Git runs")
}

// TestInstallHooksFromLinkedWorktreeWritesTrackedCopy pins AC-1: the default
// install from a linked worktree updates the worktree's tracked .beads/hooks
// copy, leaves the primary checkout clean, and keeps core.hooksPath naming the
// primary's copy (#2414).
func TestInstallHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	primary, worktree := newWorktreeHooksFixture(t, filepath.Join(".beads", "hooks"), true)
	enterWorktreeForInstallTest(t, worktree)
	setStandaloneHookMode(t, "")
	var err error
	stderr := captureStderr(t, func() { err = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
	require.NoError(t, err)
	requireHooksCurrent(t, filepath.Join(worktree, ".beads", "hooks"))
	requirePrimaryUnchanged(t, primary, filepath.Join(".beads", "hooks"))
	require.Equal(t, filepath.Join(primary, ".beads", "hooks"),
		initExcludeGit(t, worktree, "config", "--get", "core.hooksPath"),
		"core.hooksPath must keep naming the primary copy (#2414)")
	requireRedirectNote(t, stderr,
		filepath.Join(worktree, ".beads", "hooks"), filepath.Join(primary, ".beads", "hooks"))
}

// TestInstallSharedHooksFromLinkedWorktreeWritesTrackedCopy pins AC-2 for the
// committed .beads-hooks directory a --shared install targets.
func TestInstallSharedHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	primary, worktree := newWorktreeHooksFixture(t, ".beads-hooks", true)
	enterWorktreeForInstallTest(t, worktree)
	setStandaloneHookMode(t, "shared")
	var err error
	stderr := captureStderr(t, func() { err = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
	require.NoError(t, err)
	requireHooksCurrent(t, filepath.Join(worktree, ".beads-hooks"))
	requirePrimaryUnchanged(t, primary, ".beads-hooks")
	require.Equal(t, filepath.Join(primary, ".beads-hooks"),
		initExcludeGit(t, worktree, "config", "--get", "core.hooksPath"),
		"core.hooksPath must keep naming the primary copy (#2414)")
	requireRedirectNote(t, stderr,
		filepath.Join(worktree, ".beads-hooks"), filepath.Join(primary, ".beads-hooks"))
}

// TestInstallBeadsHooksFromLinkedWorktreeWritesTrackedCopy pins AC-3: --beads
// resolves the primary's .beads through the worktree fallback and still writes
// the worktree's tracked copy of it.
func TestInstallBeadsHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	primary, worktree := newWorktreeHooksFixture(t, filepath.Join(".beads", "hooks"), true)
	// The untracked database directory is what makes FindBeadsDir resolve the
	// worktree's .beads to the primary's shared copy (its step 3c).
	mkdirs(t, filepath.Join(primary, ".beads", "embeddeddolt"))
	enterWorktreeForInstallTest(t, worktree)
	setStandaloneHookMode(t, "beads")
	var err error
	stderr := captureStderr(t, func() { err = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
	require.NoError(t, err)
	requireHooksCurrent(t, filepath.Join(worktree, ".beads", "hooks"))
	requirePrimaryUnchanged(t, primary, filepath.Join(".beads", "hooks"))
	require.Equal(t, filepath.Join(primary, ".beads", "hooks"),
		initExcludeGit(t, worktree, "config", "--get", "core.hooksPath"),
		"core.hooksPath must keep naming the primary copy (#2414)")
	requireRedirectNote(t, stderr,
		filepath.Join(worktree, ".beads", "hooks"), filepath.Join(primary, ".beads", "hooks"))
}

// TestInstallHooksFromLinkedWorktreeNoteIsStderrOnly pins the stdout half of
// AC-4: the note never reaches stdout, and a --json run still prints only
// today's map.
func TestInstallHooksFromLinkedWorktreeNoteIsStderrOnly(t *testing.T) {
	primary, worktree := newWorktreeHooksFixture(t, filepath.Join(".beads", "hooks"), true)
	enterWorktreeForInstallTest(t, worktree)
	setStandaloneHookMode(t, "")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	stdout := captureStdout(t, func() error { return hooksInstallCmd.RunE(hooksInstallCmd, nil) })
	require.NotContains(t, stdout, "Note:", "the note must stay off stdout")
	require.Contains(t, stdout, `"message": "Git hooks installed successfully"`,
		"--json must still print only today's map")
	requireHooksCurrent(t, filepath.Join(worktree, ".beads", "hooks"))
	requirePrimaryUnchanged(t, primary, filepath.Join(".beads", "hooks"))
}

// TestWorktreeHooksWriteDir covers the #7725 gate's own matrix with the index
// probe stubbed: the redirect must fire only for a linked worktree and a
// primary checkout that both track the target's path, and every other layout
// keeps the directory Git runs.
func TestWorktreeHooksWriteDir(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	primary := filepath.Join(root, "main")
	worktree := filepath.Join(root, "wt")
	nested := filepath.Join(primary, ".worktrees", "wt")
	common := filepath.Join(primary, ".git")
	mkdirs(t,
		filepath.Join(primary, ".beads", "hooks"),
		filepath.Join(primary, ".beads-hooks"),
		filepath.Join(primary, "tools", "store", "hooks"),
		filepath.Join(common, "hooks"),
		filepath.Join(worktree, ".beads", "hooks"),
		filepath.Join(worktree, "tools", "store", "hooks"),
		filepath.Join(nested, ".beads", "hooks"),
		filepath.Join(root, "repo.git"),
		filepath.Join(root, "outside"),
		filepath.Join(root, "shared-hooks"),
	)

	// tracked answers indexTracks for exactly the (root, rel) pairs recorded;
	// both marks a path tracked in the primary and the linked worktree.
	tracked := map[string]bool{}
	probe := func(root, rel string) bool { return tracked[root+"\x00"+rel] }
	both := func(rel string) {
		tracked[primary+"\x00"+rel] = true
		tracked[worktree+"\x00"+rel] = true
	}
	selected := git.HooksContext{CommonDir: common, RepoRoot: worktree, MainRepoRoot: primary}

	tests := []struct {
		name   string
		setup  func()
		target string
		paths  git.HooksContext
		want   string
	}{
		{
			name:   "both indexes track .beads/hooks",
			setup:  func() { both(".beads/hooks") },
			target: filepath.Join(primary, ".beads", "hooks"),
			paths:  selected,
			want:   filepath.Join(worktree, ".beads", "hooks"),
		},
		{
			name:   "both indexes track .beads-hooks",
			setup:  func() { both(".beads-hooks") },
			target: filepath.Join(primary, ".beads-hooks"),
			paths:  selected,
			want:   filepath.Join(worktree, ".beads-hooks"),
		},
		{
			name:   "an in-repo store keeps its path relative to the primary",
			setup:  func() { both("tools/store/hooks") },
			target: filepath.Join(primary, "tools", "store", "hooks"),
			paths:  selected,
			want:   filepath.Join(worktree, "tools", "store", "hooks"),
		},
		{
			// Mirroring only the leaf would dump the hooks at a path no commit
			// has ever contained, and leave the tracked copy stale.
			name: "a worktree nested under the primary redirects to its own copy",
			setup: func() {
				both(".beads/hooks")
				tracked[nested+"\x00"+".beads/hooks"] = true
			},
			target: filepath.Join(primary, ".beads", "hooks"),
			paths:  git.HooksContext{CommonDir: common, RepoRoot: nested, MainRepoRoot: primary},
			want:   filepath.Join(nested, ".beads", "hooks"),
		},
		{
			name:   "only the worktree tracks the directory",
			setup:  func() { tracked[worktree+"\x00"+".beads/hooks"] = true },
			target: filepath.Join(primary, ".beads", "hooks"),
			paths:  selected,
			want:   filepath.Join(primary, ".beads", "hooks"),
		},
		{
			name:   "only the primary tracks the directory",
			setup:  func() { tracked[primary+"\x00"+".beads/hooks"] = true },
			target: filepath.Join(primary, ".beads", "hooks"),
			paths:  selected,
			want:   filepath.Join(primary, ".beads", "hooks"),
		},
		{
			name:   "neither index tracks the directory",
			setup:  func() {},
			target: filepath.Join(primary, ".beads", "hooks"),
			paths:  selected,
			want:   filepath.Join(primary, ".beads", "hooks"),
		},
		{
			// The shared .git/hooks is one per common dir; every worktree runs
			// it, so it must never move into one worktree.
			name:   "the common dir's own hooks stay shared",
			setup:  func() { both(".git/hooks") },
			target: filepath.Join(common, "hooks"),
			paths:  selected,
			want:   filepath.Join(common, "hooks"),
		},
		{
			name:   "a target outside the primary stays put",
			setup:  func() { both("outside") },
			target: filepath.Join(root, "outside"),
			paths:  selected,
			want:   filepath.Join(root, "outside"),
		},
		{
			name:   "a target already inside the worktree stays put",
			setup:  func() { both(".beads/hooks") },
			target: filepath.Join(worktree, ".beads", "hooks"),
			paths:  selected,
			want:   filepath.Join(worktree, ".beads", "hooks"),
		},
		{
			// A bare repository has no working tree, so there is no tracked
			// path to mirror; its worktrees keep writing the shared copy.
			name:   "a bare-backed common dir has no primary checkout",
			setup:  func() { both("shared-hooks") },
			target: filepath.Join(root, "shared-hooks"),
			paths:  git.HooksContext{CommonDir: filepath.Join(root, "repo.git"), RepoRoot: worktree, MainRepoRoot: root},
			want:   filepath.Join(root, "shared-hooks"),
		},
		{
			// The work-tree-less fallback of resolveStandaloneHooksContext.
			name:   "a work-tree-less context has no common dir",
			setup:  func() { both(".beads/hooks") },
			target: filepath.Join(primary, ".beads", "hooks"),
			paths:  git.HooksContext{HooksDir: filepath.Join(primary, ".beads", "hooks"), RepoRoot: worktree, MainRepoRoot: primary},
			want:   filepath.Join(primary, ".beads", "hooks"),
		},
		{
			name:   "an install from the primary itself stays put",
			setup:  func() { both(".beads/hooks") },
			target: filepath.Join(primary, ".beads", "hooks"),
			paths:  git.HooksContext{CommonDir: common, RepoRoot: primary, MainRepoRoot: primary},
			want:   filepath.Join(primary, ".beads", "hooks"),
		},
		{
			name:   "the primary root itself is never a mirror target",
			setup:  func() { both(".beads/hooks") },
			target: primary,
			paths:  selected,
			want:   primary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clear(tracked)
			tt.setup()
			if got := worktreeHooksWriteDir(tt.target, tt.paths, probe); got != tt.want {
				t.Fatalf("worktreeHooksWriteDir(%q, %+v) = %q, want %q", tt.target, tt.paths, got, tt.want)
			}
		})
	}
}

// TestInstallHooksFromLinkedWorktreeKeepsUntrackedPrimaryCopy pins the
// declining side of the gate (AC-5): a primary .beads/hooks that no index
// tracks keeps today's behavior — the install writes the primary's copy,
// creates nothing under the worktree, and prints no note. It passes before and
// after the fix by design.
func TestInstallHooksFromLinkedWorktreeKeepsUntrackedPrimaryCopy(t *testing.T) {
	primary, worktree := newWorktreeHooksFixture(t, filepath.Join(".beads", "hooks"), false)
	enterWorktreeForInstallTest(t, worktree)
	setStandaloneHookMode(t, "")
	var err error
	stderr := captureStderr(t, func() { err = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
	require.NoError(t, err)
	require.NotContains(t, stderr, "Note:")
	requireHooksCurrent(t, filepath.Join(primary, ".beads", "hooks"))
	_, statErr := os.Stat(filepath.Join(worktree, ".beads"))
	require.True(t, os.IsNotExist(statErr), "an untracked primary copy must not grow a worktree mirror")
}

// TestInstallHooksFromLinkedWorktreeIgnoresInheritedIndex pins the other
// declining side (AC-5): the redirect must key on the selected,
// routing-scrubbed environment. An inherited GIT_INDEX_FILE naming a decoy
// repository whose index tracks the path must not manufacture a redirect. It
// passes before and after the fix by design.
func TestInstallHooksFromLinkedWorktreeIgnoresInheritedIndex(t *testing.T) {
	primary, worktree := newWorktreeHooksFixture(t, filepath.Join(".beads", "hooks"), false)
	root := filepath.Dir(primary)
	decoy := filepath.Join(root, "decoy")
	mkdirs(t, filepath.Join(decoy, ".beads", "hooks"))
	runGitForWorktreeTest(t, decoy, "init", "-q", "-b", "main")
	runGitForWorktreeTest(t, decoy, "config", "user.email", "test@example.com")
	runGitForWorktreeTest(t, decoy, "config", "user.name", "test")
	for _, name := range managedHookNames {
		writeOldMarkerHook(t, filepath.Join(decoy, ".beads", "hooks"), name)
	}
	runGitForWorktreeTest(t, decoy, "add", ".beads/hooks")
	runGitForWorktreeTest(t, decoy, "commit", "-q", "-m", "track hooks")

	enterWorktreeForInstallTest(t, worktree)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	setStandaloneHookMode(t, "")
	var err error
	stderr := captureStderr(t, func() { err = hooksInstallCmd.RunE(hooksInstallCmd, nil) })
	require.NoError(t, err)
	require.NotContains(t, stderr, "Note:")
	requireHooksCurrent(t, filepath.Join(primary, ".beads", "hooks"))
	_, statErr := os.Stat(filepath.Join(worktree, ".beads"))
	require.True(t, os.IsNotExist(statErr), "inherited routing must not manufacture a redirect target")
}
