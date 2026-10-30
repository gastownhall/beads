package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
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
	root := t.TempDir()
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
