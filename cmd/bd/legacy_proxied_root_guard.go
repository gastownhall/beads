package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// legacyProxiedRootName is the proxied-server data root bd v1.0.5, v1.1.0
// through v1.1.2 and v1.2.2 defaulted to (<beadsDir>/proxieddb) when no
// environment variable or sidecar named another. Current builds resolve the
// default root through doltserver.ResolveDoltDir (<beadsDir>/dolt), and
// default proxied init writes no sidecar, so an older bd run in a workspace
// this build created finds nothing at proxieddb, initializes a fresh database
// there, and reports "No issues found." with exit 0 (GH#5104).
const legacyProxiedRootName = "proxieddb"

// legacyProxiedRootGitTimeout bounds the read-only git check-ignore the guard
// runs; the guard is best-effort, so a slow git skips it rather than delaying
// the command.
const legacyProxiedRootGitTimeout = 5 * time.Second

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
// a dangling one) exists at the path, when .beads/.gitignore holds the
// standalone "proxieddb" pattern, and when git confirms the path is
// effectively ignored there (git check-ignore: a later negation, a tracked
// path, no Git worktree, or a git failure all skip it), so the marker never
// shows up as an untracked file to commit. The file is created exclusively
// and never replaced, truncated, followed or removed afterward.
func armLegacyProxiedRootGuard(ctx context.Context, beadsDir string) {
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
	if !beadsGitignoreHasStandalonePattern(beadsDir, legacyProxiedRootName) {
		debug.Logf("legacy proxied root guard: %s has no standalone %q pattern; not arming\n", filepath.Join(beadsDir, ".gitignore"), legacyProxiedRootName)
		return
	}
	if ignored, err := legacyProxiedRootIgnoredByGit(ctx, beadsDir); err != nil || !ignored {
		debug.Logf("legacy proxied root guard: %s is not effectively git-ignored (err=%v); not arming\n", path, err)
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
// managed-local proxied-server open, the only topology whose default root an
// older bd would have resolved to proxieddb. An external topology has no local
// root, a failed open leaves the workspace as it found it, and a preview open
// (opts carry uow.WithPreview: --dry-run, --inspect) writes nothing before its
// command runs; the next ordinary open arms it.
func armLegacyProxiedRootGuardAfterOpen(ctx context.Context, beadsDir string, topology sqlServerUOWTopology, opts []uow.ProviderOption, openErr error) {
	if openErr != nil || topology.external != nil || uow.OpensForPreview(opts...) {
		return
	}
	armLegacyProxiedRootGuard(ctx, beadsDir)
}

// beadsGitignoreHasStandalonePattern reports whether beadsDir/.gitignore holds
// pattern as a line of its own (surrounding whitespace aside), as
// containsGitignorePattern in cmd/bd/doctor reads it. "proxieddb/" does not
// count: it ignores only a directory, and the marker is a file.
func beadsGitignoreHasStandalonePattern(beadsDir, pattern string) bool {
	content, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore")) // #nosec G304 -- fixed name under the active .beads dir
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

// runLegacyProxiedRootGitProbe runs one read-only git command for the guard
// in dir. gitCmdInDir scrubs Git's routing environment (gitenv routing keys:
// GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, ...), so the probe answers for the
// repository dir belongs to, never one an inherited variable names, and it
// disables hooks and templates. It is a variable so tests can land a
// concurrent writer between the probe and the guard's create.
var runLegacyProxiedRootGitProbe = func(ctx context.Context, dir string, args ...string) error {
	return gitCmdInDir(ctx, dir, args...).Run()
}

// legacyProxiedRootIgnoredByGit runs a read-only `git check-ignore` for the
// legacy root in beadsDir's worktree. Without --no-index a tracked path is
// reported as not ignored, and outside a Git worktree the command fails, so
// both skip the guard.
func legacyProxiedRootIgnoredByGit(ctx context.Context, beadsDir string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, legacyProxiedRootGitTimeout)
	defer cancel()
	err := runLegacyProxiedRootGitProbe(ctx, beadsDir, "check-ignore", "-q", "--", legacyProxiedRootName)
	if err == nil {
		return true, nil
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
