package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/stretchr/testify/require"
)

// Repro for PR #7725: `bd hooks install` run from a linked worktree must write
// the worktree's own tracked .beads/hooks copy, not dirty the primary checkout.
func TestReproInstallHooksFromLinkedWorktreeWritesTrackedCopy(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	require.NoError(t, os.MkdirAll(mainDir, 0755))
	initExcludeGit(t, mainDir, "init", "-q", "-b", "main")
	initExcludeGit(t, mainDir, "config", "user.email", "r@example.com")
	initExcludeGit(t, mainDir, "config", "user.name", "r")
	initExcludeGit(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")
	primaryHooks := filepath.Join(mainDir, ".beads", "hooks")
	require.NoError(t, os.MkdirAll(primaryHooks, 0755))
	initExcludeGit(t, mainDir, "config", "core.hooksPath", primaryHooks)

	t.Chdir(mainDir)
	git.ResetCaches()
	require.NoError(t, installStandaloneHooks(managedHookNames, false, false, false, false))

	// Age the committed marker so a reinstall must rewrite every hook.
	old := regexp.MustCompile(`BEGIN BEADS INTEGRATION v[0-9.]+`)
	for _, name := range managedHookNames {
		p := filepath.Join(primaryHooks, name)
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, old.ReplaceAll(data, []byte("BEGIN BEADS INTEGRATION v0.0.1")), 0755))
	}
	initExcludeGit(t, mainDir, "add", ".beads/hooks")
	initExcludeGit(t, mainDir, "commit", "-q", "-m", "track hooks")

	wt := filepath.Join(root, "wt")
	initExcludeGit(t, mainDir, "worktree", "add", "-q", wt, "-b", "wt")
	t.Chdir(wt)
	git.ResetCaches()
	require.NoError(t, installStandaloneHooks(managedHookNames, false, false, false, false))

	require.Empty(t, initExcludeGit(t, mainDir, "status", "--porcelain"),
		"install from the linked worktree dirtied the primary checkout")
	for _, name := range managedHookNames {
		data, err := os.ReadFile(filepath.Join(wt, ".beads", "hooks", name))
		require.NoError(t, err)
		require.False(t, strings.Contains(string(data), "v0.0.1"),
			"worktree's tracked %s still carries the stale marker", name)
	}
	got := initExcludeGit(t, wt, "config", "--get", "core.hooksPath")
	require.Equal(t, primaryHooks, got, "core.hooksPath must keep naming the primary copy (#2414)")
}
