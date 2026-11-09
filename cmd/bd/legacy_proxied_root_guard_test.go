package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/require"
)

// legacyProxiedRootGuardWorkspace is the fixture the guard exists for: a Git
// worktree whose .beads directory carries the canonical .gitignore, the way
// bd init leaves it. It returns the project root and the .beads directory.
func legacyProxiedRootGuardWorkspace(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	initGitRepoAt(t, root)
	beadsDir := filepath.Join(root, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(beadsDir, ".gitignore"), []byte(doctor.GitignoreTemplate), 0o600))
	return root, beadsDir
}

// gitStatusIn returns git status --porcelain output for the guard path in
// dir, on the scrubbed env the guard's own probes use, so the test judges
// ignored-ness the way git itself does. -uall stops git from collapsing the
// untracked .beads/ directory to one `?? .beads/` line, which would hide
// whether the marker itself is offered.
func gitStatusIn(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain", "-uall", "--", ".beads/"+legacyProxiedRootGuardName)
	cmd.Dir = dir
	cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git status: %s", out)
	return string(out)
}

// TestLegacyProxiedRootGuardArmsManagedLocalWorkspace pins the guard's happy
// path (GH#5104): on a proxied-server workspace inside a Git worktree, arming
// leaves a REGULAR file at .beads/proxieddb — the path pre-layout releases
// resolved the proxied root to — that git ignores, that tells the operator
// what it is, and that the current root layout never consults, so the
// workspace's own commands keep working.
func TestLegacyProxiedRootGuardArmsManagedLocalWorkspace(t *testing.T) {
	root, beadsDir := legacyProxiedRootGuardWorkspace(t)

	armLegacyProxiedRootGuard(beadsDir)

	guardPath := filepath.Join(beadsDir, legacyProxiedRootGuardName)
	info, err := os.Lstat(guardPath)
	require.NoError(t, err, "arming left no guard file")
	if !info.Mode().IsRegular() {
		t.Fatalf("guard %s is not a regular file: %v", guardPath, info.Mode())
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("guard mode %04o grants group/other access; the ignore rules do not make a world-readable file safe", info.Mode().Perm())
	}

	content, err := os.ReadFile(guardPath)
	require.NoError(t, err)
	// The note identifies the build that wrote it and explains the upgrade
	// path; an operator who finds the file must not think it is database
	// residue and delete it.
	for _, want := range []string{"bd", Version, "GH#5104", "Do not delete", "Upgrading"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("guard note missing %q:\n%s", want, content)
		}
	}

	// Git ignores the marker where it lives and offers nothing to commit:
	// the guard protects the workspace without entering its history.
	cmd := exec.Command("git", "check-ignore", "-q", "--", ".beads/"+legacyProxiedRootGuardName)
	cmd.Dir = root
	cmd.Env = testutil.WithoutGitRepoLocationEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		t.Errorf("git does not ignore the guard file: %v\n%s", err, gitStatusIn(t, root))
	}
	if status := gitStatusIn(t, root); status != "" {
		t.Errorf("git offers the guard file for commit:\n%s", status)
	}

	// Commands still work: the current root layout resolves to the data dir
	// bd actually uses (.beads/dolt), never to the obsolete root the guard
	// occupies, so the marker cannot send a current bd at the wrong path.
	resolved, err := doltserver.ResolveProxiedServerRootPath(beadsDir)
	require.NoError(t, err)
	if resolved == guardPath || strings.HasSuffix(resolved, legacyProxiedRootGuardName) {
		t.Errorf("root resolution %s points at the guard file %s", resolved, guardPath)
	}
	if err := validateProxiedServerRootPath(resolved); err != nil {
		t.Errorf("current root %s no longer validates with the guard present: %v", resolved, err)
	}
}

// TestArmLegacyProxiedRootGuardAfterOpenWiring pins the open-path wiring: a
// managed-local proxied topology arms the guard for its workspace, and an
// external topology — whose database lives on another machine — never does.
func TestArmLegacyProxiedRootGuardAfterOpenWiring(t *testing.T) {
	var armed []string
	prev := armLegacyProxiedRootGuard
	armLegacyProxiedRootGuard = func(beadsDir string) { armed = append(armed, beadsDir) }
	t.Cleanup(func() { armLegacyProxiedRootGuard = prev })

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	armLegacyProxiedRootGuardAfterOpen(beadsDir, sqlServerUOWTopology{})
	require.Equal(t, []string{beadsDir}, armed,
		"a managed-local proxied open must arm the guard for its workspace")

	armed = nil
	armLegacyProxiedRootGuardAfterOpen(beadsDir, sqlServerUOWTopology{
		external: &configfile.ExternalDoltConfig{Host: "db.invalid", Port: 1},
	})
	require.Empty(t, armed,
		"an external proxied topology must not arm the guard: the workspace only points at a database on another machine")
}
