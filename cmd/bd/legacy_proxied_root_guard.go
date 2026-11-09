package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/debug"
)

// legacyProxiedRootName is the proxied-server data root bd releases up to
// v1.2.x defaulted to (<beadsDir>/proxieddb) when no environment variable or
// sidecar named another. Current builds resolve the default root through
// doltserver.ResolveDoltDir (<beadsDir>/dolt), and default proxied init
// writes no sidecar, so an older bd run in a workspace this build created
// finds nothing at proxieddb, initializes a fresh database there, and
// reports "No issues found." with exit 0 (GH#5104).
const legacyProxiedRootName = "proxieddb"

// legacyProxiedRootGuardText is the content of the marker file. Its being a
// regular file is what makes an older bd refuse ("proxieddb: not a
// directory"); the text is for the human who finds it.
func legacyProxiedRootGuardText() string {
	return fmt.Sprintf(`This file is not a database, and it is not a directory on purpose.

This workspace uses proxied-server mode, and its Dolt data lives in the root
the current bd resolves (by default .beads/dolt), not here. Older bd releases
defaulted the proxied-server root to .beads/proxieddb: run in this workspace,
such a bd would create a new, empty database at this path and report
"No issues found." while the real issues sit elsewhere.

With this file in place, such a bd refuses with "proxieddb: not a directory"
instead. That error means the bd that printed it is too old for this
workspace: upgrade every bd that opens it (on PATH, in git hooks, in CI and
agent tooling) to a build at least as new as the one that wrote this file.

Written by bd %s. It is ignored by git (see .beads/.gitignore) and is safe to
leave in place; delete it only once no older bd can run here.
`, Version)
}

// armLegacyProxiedRootGuard places a regular file at <beadsDir>/proxieddb so
// that an older bd, whose default proxied-server root is that path, refuses
// the workspace ("not a directory") instead of creating an unrelated empty
// database there. It is best-effort and never changes the outcome of the open
// that called it: every failure is debug-logged and skipped.
//
// It creates the file only when nothing (no file, directory or symlink, even
// a dangling one) exists at the path. The file is created exclusively and
// never replaced, truncated, followed or removed afterward.
func armLegacyProxiedRootGuard(beadsDir string) {
	if beadsDir == "" {
		return
	}
	path := filepath.Join(beadsDir, legacyProxiedRootName)
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			debug.Logf("legacy proxied root guard: stat %s: %v\n", path, err)
		}
		return
	}
	// O_EXCL fails on any existing entry, a symlink (dangling or not)
	// included, so a concurrent creator or a racing directory is left alone.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- fixed name under the active .beads dir
	if err != nil {
		if !errors.Is(err, fs.ErrExist) {
			debug.Logf("legacy proxied root guard: create %s: %v\n", path, err)
		}
		return
	}
	// A failed write leaves a short or empty regular file, which still makes
	// an older bd refuse; it is not removed, since the path could have been
	// replaced in between.
	if _, err := f.WriteString(legacyProxiedRootGuardText()); err != nil {
		debug.Logf("legacy proxied root guard: write %s: %v\n", path, err)
	}
	if err := f.Close(); err != nil {
		debug.Logf("legacy proxied root guard: close %s: %v\n", path, err)
	}
}

// armLegacyProxiedRootGuardAfterOpen arms the guard after a successful
// proxied-server open; a failed open leaves the workspace as it found it.
func armLegacyProxiedRootGuardAfterOpen(beadsDir string, openErr error) {
	if openErr != nil {
		return
	}
	armLegacyProxiedRootGuard(beadsDir)
}
