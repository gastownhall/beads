package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestInstallHooksFromLinkedWorktreeWritesTrackedCopy pins AC-1: the default
// install from a linked worktree updates the worktree's tracked .beads/hooks
// copy, leaves the primary checkout clean, and keeps core.hooksPath naming the
// primary's copy (#2414).
func TestInstallHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	primary, worktree := newWorktreeHooksFixture(t, filepath.Join(".beads", "hooks"), true)
	enterWorktreeForInstallTest(t, worktree)
	setStandaloneHookMode(t, "")
	require.NoError(t, hooksInstallCmd.RunE(hooksInstallCmd, nil))
	requireHooksCurrent(t, filepath.Join(worktree, ".beads", "hooks"))
	requirePrimaryUnchanged(t, primary, filepath.Join(".beads", "hooks"))
	require.Equal(t, filepath.Join(primary, ".beads", "hooks"),
		initExcludeGit(t, worktree, "config", "--get", "core.hooksPath"),
		"core.hooksPath must keep naming the primary copy (#2414)")
}
