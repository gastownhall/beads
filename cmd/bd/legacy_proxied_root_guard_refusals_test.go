package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/stretchr/testify/require"
)

// guardPathFor returns the guard path for a fixture .beads dir.
func guardPathFor(beadsDir string) string {
	return filepath.Join(beadsDir, legacyProxiedRootGuardName)
}

// assertNoGuardArmed fails when arming left a guard entry behind: every
// refusal in this file must leave the workspace untouched.
func assertNoGuardArmed(t *testing.T, beadsDir string) {
	t.Helper()
	if _, err := os.Lstat(guardPathFor(beadsDir)); !os.IsNotExist(err) {
		t.Errorf("arming left a guard entry behind (stat error: %v)", err)
	}
}

// TestLegacyProxiedRootGuardLeavesExistingEntries pins gate 1: the guard
// only ever creates its path. Whatever already sits at
// <beadsDir>/proxieddb — a legacy release's real directory, another
// writer's file, a symlink, even a dangling one — is left byte-for-byte as
// it is.
func TestLegacyProxiedRootGuardLeavesExistingEntries(t *testing.T) {
	t.Run("existing file", func(t *testing.T) {
		_, beadsDir := legacyProxiedRootGuardWorkspace(t)
		const keep = "another writer was here"
		require.NoError(t, os.WriteFile(guardPathFor(beadsDir), []byte(keep), 0o600))

		armLegacyProxiedRootGuard(beadsDir)

		got, err := os.ReadFile(guardPathFor(beadsDir))
		require.NoError(t, err)
		require.Equal(t, keep, string(got), "arming replaced another writer's file")
	})

	t.Run("existing directory", func(t *testing.T) {
		_, beadsDir := legacyProxiedRootGuardWorkspace(t)
		require.NoError(t, os.Mkdir(guardPathFor(beadsDir), 0o700))

		armLegacyProxiedRootGuard(beadsDir)

		info, err := os.Lstat(guardPathFor(beadsDir))
		require.NoError(t, err)
		require.True(t, info.IsDir(), "arming replaced a directory a legacy release may still be using")
	})

	t.Run("dangling symlink", func(t *testing.T) {
		_, beadsDir := legacyProxiedRootGuardWorkspace(t)
		require.NoError(t, os.Symlink(
			filepath.Join(t.TempDir(), "never-created"), guardPathFor(beadsDir)))

		armLegacyProxiedRootGuard(beadsDir)

		info, err := os.Lstat(guardPathFor(beadsDir))
		require.NoError(t, err)
		require.Equal(t, os.ModeSymlink, info.Mode()&os.ModeSymlink, "arming replaced a symlink with a regular file")
	})
}

// TestLegacyProxiedRootGuardRefusesIneffectiveIgnore pins gate 2's
// check-ignore half: the guard is armed only where git itself already
// considers the marker path ignored. A workspace whose ignore rules do not
// ignore a REGULAR file at .beads/proxieddb — because bd never wrote the
// standalone pattern, because it predates it with the directory-only form,
// because a negation rule wins, because there is no Git worktree at all, or
// because git itself cannot answer — gets no guard, not a file git would
// offer to commit.
func TestLegacyProxiedRootGuardRefusesIneffectiveIgnore(t *testing.T) {
	t.Run("gitignore predates the standalone pattern", func(t *testing.T) {
		_, beadsDir := legacyProxiedRootGuardWorkspace(t)
		// The directory-only form bd wrote before this change: it does not
		// ignore the regular file the guard would create.
		require.NoError(t, os.WriteFile(
			filepath.Join(beadsDir, ".gitignore"), []byte("proxieddb/\n"), 0o600))

		armLegacyProxiedRootGuard(beadsDir)

		assertNoGuardArmed(t, beadsDir)
	})

	t.Run("negation rule wins", func(t *testing.T) {
		_, beadsDir := legacyProxiedRootGuardWorkspace(t)
		require.NoError(t, os.WriteFile(
			filepath.Join(beadsDir, ".gitignore"), []byte("proxieddb\n!proxieddb\n"), 0o600))

		armLegacyProxiedRootGuard(beadsDir)

		assertNoGuardArmed(t, beadsDir)
	})

	t.Run("no beads gitignore at all", func(t *testing.T) {
		root := t.TempDir()
		initGitRepoAt(t, root)
		beadsDir := filepath.Join(root, ".beads")
		require.NoError(t, os.MkdirAll(beadsDir, 0o700))

		armLegacyProxiedRootGuard(beadsDir)

		assertNoGuardArmed(t, beadsDir)
	})

	t.Run("outside any Git worktree", func(t *testing.T) {
		// A .beads dir with the canonical gitignore but no enclosing repo:
		// nothing ignores the marker, so there is no guard to write.
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		require.NoError(t, os.MkdirAll(beadsDir, 0o700))
		require.NoError(t, os.WriteFile(
			filepath.Join(beadsDir, ".gitignore"), []byte(doctor.GitignoreTemplate), 0o600))

		armLegacyProxiedRootGuard(beadsDir)

		assertNoGuardArmed(t, beadsDir)
	})
}

// TestLegacyProxiedRootGuardRefusesTrackedPath pins gate 2's ls-files half:
// check-ignore pattern-matches tracked files too, so a path some release
// force-added to the index — present to git, absent from disk — is not one
// to shadow with a guard file.
func TestLegacyProxiedRootGuardRefusesTrackedPath(t *testing.T) {
	root, beadsDir := legacyProxiedRootGuardWorkspace(t)
	require.NoError(t, os.WriteFile(guardPathFor(beadsDir), []byte("tracked\n"), 0o600))
	runGitForBootstrapTest(t, root, "add", "-f", filepath.Join(".beads", legacyProxiedRootGuardName))
	require.NoError(t, os.Remove(guardPathFor(beadsDir)))

	armLegacyProxiedRootGuard(beadsDir)

	assertNoGuardArmed(t, beadsDir)
}

// TestLegacyProxiedRootGuardRefusesWhenGitFails pins the lookup-failure arm:
// a git that cannot answer — corrupted repo, missing binary, a hostile
// filesystem — means the ignore status is unknown, and an unknown is not a
// yes. The probe var is the seam; the real git here is fine.
func TestLegacyProxiedRootGuardRefusesWhenGitFails(t *testing.T) {
	_, beadsDir := legacyProxiedRootGuardWorkspace(t)
	prev := runLegacyProxiedRootGitProbe
	runLegacyProxiedRootGitProbe = func(string, ...string) ([]byte, error) {
		return nil, errors.New("git: fatal: not a git repository")
	}
	t.Cleanup(func() { runLegacyProxiedRootGitProbe = prev })

	armLegacyProxiedRootGuard(beadsDir)

	assertNoGuardArmed(t, beadsDir)
}

// TestLegacyProxiedRootGuardLosesToConcurrentCreation pins gate 3's O_EXCL:
// when another writer creates the path between the guard's probes and its
// create, the guard yields — the other writer's file is left exactly as
// written. The probe var is the seam that simulates the losing race.
func TestLegacyProxiedRootGuardLosesToConcurrentCreation(t *testing.T) {
	_, beadsDir := legacyProxiedRootGuardWorkspace(t)
	const concurrent = "written by the winner"
	prev := runLegacyProxiedRootGitProbe
	runLegacyProxiedRootGitProbe = func(string, ...string) ([]byte, error) {
		if _, err := os.Lstat(guardPathFor(beadsDir)); os.IsNotExist(err) {
			// The concurrent writer lands between the guard's probes and
			// its create.
			require.NoError(t, os.WriteFile(guardPathFor(beadsDir), []byte(concurrent), 0o600))
		}
		return nil, nil
	}
	t.Cleanup(func() { runLegacyProxiedRootGitProbe = prev })

	armLegacyProxiedRootGuard(beadsDir)

	got, err := os.ReadFile(guardPathFor(beadsDir))
	require.NoError(t, err)
	require.Equal(t, concurrent, string(got), "arming overwrote the concurrent writer's file")
}

// TestLegacyProxiedRootGuardToleratesUnwritableBeadsDir pins gate 3's
// failure arm: a .beads bd cannot write into (a read-only mount, a full
// disk) is skipped like every other unmet precondition — no error, no
// entry, and the open that carried the arming still succeeded.
func TestLegacyProxiedRootGuardToleratesUnwritableBeadsDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0500 does not deny writes")
	}
	_, beadsDir := legacyProxiedRootGuardWorkspace(t)
	require.NoError(t, os.Chmod(beadsDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(beadsDir, 0o700) })

	armLegacyProxiedRootGuard(beadsDir)

	assertNoGuardArmed(t, beadsDir)
}

// TestFailedProxiedOpenDoesNotArmGuard pins the wiring's placement: the
// arming happens only after the provider open SUCCEEDS. A refused open —
// here by a corrupt workspace config — must leave the guard unarmed; an
// external topology's refusal is the same placement, pinned by
// TestArmLegacyProxiedRootGuardAfterOpenWiring.
func TestFailedProxiedOpenDoesNotArmGuard(t *testing.T) {
	var armed []string
	prev := armLegacyProxiedRootGuard
	armLegacyProxiedRootGuard = func(beadsDir string) { armed = append(armed, beadsDir) }
	t.Cleanup(func() { armLegacyProxiedRootGuard = prev })

	beadsDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(beadsDir, configfile.ConfigFileName), []byte("{not json"), 0o600))

	_, err := newProxiedServerUOWProvider(t.Context(), beadsDir, "")
	require.Error(t, err, "the corrupt-config open unexpectedly succeeded")
	require.Empty(t, armed, "a failed provider open armed the guard")
}
