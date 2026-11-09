package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/steveyegge/beads/internal/gitenv"
)

// legacyProxiedRootGuardName is the file name of the legacy proxied-root
// guard marker, at <beadsDir>/proxieddb — the path pre-proxied-layout bd
// releases (v1.1.x) resolved a proxied-server workspace's root to.
const legacyProxiedRootGuardName = "proxieddb"

// armLegacyProxiedRootGuard arms the legacy proxied-root guard at
// <beadsDir>/proxieddb, best-effort: it never reports a failure, because the
// guard protects an old-release hazard (GH#5104) and must not make the open
// that carries it fail. It is a variable so the open-path wiring tests can
// observe (or replace) the arming; the implementation is
// armLegacyProxiedRootGuardAt.
var armLegacyProxiedRootGuard = armLegacyProxiedRootGuardAt

// runLegacyProxiedRootGitProbe runs one read-only git probe (check-ignore,
// ls-files) for the guard in dir, with git's routing environment scrubbed so
// the probe answers for the repository dir belongs to, never one an
// inherited GIT_DIR names. It is a variable so tests can force the
// lookup-failure arm of the guard.
var runLegacyProxiedRootGitProbe = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...) // #nosec G204 - args are hardcoded probes
	cmd.Dir = dir
	cmd.Env = gitenv.ScrubRouting(os.Environ())
	return cmd.Output()
}

// armLegacyProxiedRootGuardAt is the guard's implementation. A proxied-server
// workspace initialized by a current bd keeps its Dolt database at the current
// root layout (.beads/dolt by default), but a bd release that predates that
// layout resolved the proxied root to <beadsDir>/proxieddb and, finding no
// database there, silently fell through to the embedded store and reported
// the workspace's real tracker as empty with exit 0 (GH#5104). A regular file
// at that path makes those releases refuse with "not a directory" before they
// open or create any storage — the same seam validateProxiedServerRootPath
// already refuses on.
//
// Because the marker is a defense the workspace carries with it, arming has
// to be conservative on every axis:
//
//   - it only ever CREATES the path (O_CREATE|O_EXCL, 0600): an existing
//     entry — a directory a legacy release actually used, a dangling symlink,
//     another writer's file — is left exactly as it is;
//   - the path must ALREADY be effectively git-ignored where it lives: the
//     workspace's .beads/.gitignore must carry the standalone `proxieddb`
//     pattern (the directory-only `proxieddb/` form does not ignore a regular
//     file), and a real `git check-ignore -q` must agree — which also
//     refuses the arming when the ignore rules negate the pattern, outside a
//     Git worktree, or when git itself fails — and `git ls-files` must not
//     report the path tracked. The guard never repairs the .gitignore
//     itself: making a file git would offer to commit is a write the guard
//     has no business making, and doctor's append-only repair plus new
//     inits put the pattern there first;
//   - a file that is created but cannot be written (a full disk, a hostile
//     mount) is left in place: an empty regular file stops the old release
//     just as well.
//
// The git probes run from the .beads parent so their path argument is the
// one git itself would show for the marker (.beads/proxieddb); git resolves
// it against the enclosing worktree wherever that worktree's root is.
func armLegacyProxiedRootGuardAt(beadsDir string) {
	guardPath := filepath.Join(beadsDir, legacyProxiedRootGuardName)
	// Gate 1: only an absent path is armed. Any existing entry — including a
	// dangling symlink, which Stat would miss — means the workspace is not
	// ours to write here.
	if _, err := os.Lstat(guardPath); err == nil || !os.IsNotExist(err) {
		return
	}

	// Gate 2: git must consider the marker path ignored where it lives.
	// check-ignore exits 0 only when a pattern matches, which is also the
	// answer for a workspace whose .beads/.gitignore predates the standalone
	// pattern (a directory-only proxieddb/ line does not ignore a regular
	// file), for a workspace whose ignore rules negate the pattern, for a
	// directory outside any Git worktree, and for a git that cannot run.
	repoDir := filepath.Dir(beadsDir)
	relGuard := filepath.ToSlash(filepath.Join(filepath.Base(beadsDir), legacyProxiedRootGuardName))
	if _, err := runLegacyProxiedRootGitProbe(repoDir, "check-ignore", "-q", "--", relGuard); err != nil {
		return
	}
	// check-ignore pattern-matches tracked files too, so the tracked status
	// needs its own probe: a path some release force-added is not one to
	// shadow with a guard file.
	if out, err := runLegacyProxiedRootGitProbe(repoDir, "ls-files", "--", relGuard); err != nil || len(bytes.TrimSpace(out)) > 0 {
		return
	}

	// Gate 3: create-or-nothing. O_EXCL means a concurrent arming, or any
	// entry that appeared since the Lstat, leaves the other writer's file
	// alone; a creation failure (a read-only .beads, a full disk) is the
	// same silent skip as every other unmet precondition.
	file, err := os.OpenFile(guardPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- the caller supplies the active .beads dir; the name beside it is this file's constant
	if err != nil {
		return
	}
	defer file.Close() // #nosec G307 - best-effort arming; a leak is impossible, file is closed at return
	// A short write is tolerated for the same reason a failed creation is:
	// the regular file, even empty, is the guard.
	_, _ = file.WriteString(legacyProxiedRootGuardNote())
}

// legacyProxiedRootGuardNote is the marker's content: it identifies the bd
// build that wrote it, says what the file protects against, and tells the
// operator what to do about it — upgrade the older binary, never delete the
// guard or turn it back into a directory.
func legacyProxiedRootGuardNote() string {
	return fmt.Sprintf(`This file is a compatibility guard written by bd %s.

This workspace uses proxied-server mode with its Dolt database at the
current root layout. bd releases that predate that layout resolved the
proxied root to this path and, finding no database here, silently reported
an empty tracker with exit 0 (GH#5104). As a regular file, this guard makes
those releases refuse with an error instead of reporting no issues.

Do not delete this file, do not commit it (it is git-ignored), and do not
replace it with a directory. Upgrading the older bd binary to the latest
release makes it read this workspace correctly; keep this guard so other
machines still running the older release are protected too.
`, Version)
}

// armLegacyProxiedRootGuardAfterOpen arms the guard for an open that has just
// succeeded. The guard exists to protect against OLD releases on THIS
// workspace, so it arms only for the topology whose database lives in the
// workspace itself — a managed-local proxied open. An external topology's
// database is on another machine, and a workspace that only points at it is
// not one this bd can speak for.
func armLegacyProxiedRootGuardAfterOpen(beadsDir string, topology sqlServerUOWTopology) {
	if topology.external != nil {
		return
	}
	armLegacyProxiedRootGuard(beadsDir)
}
