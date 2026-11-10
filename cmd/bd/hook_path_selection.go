package main

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// GH#5922 (install-time arm): the installed hook shims resolve bd from PATH
// inside git's non-interactive shell (`command -v bd`), so the bd a commit
// runs can differ from the bd that installed the hooks — the installer may
// itself have been started by absolute path, an alias or a shell function.
// The shims stay deliberately portable (no host-specific paths are pinned
// into tracked hook files), so the one moment the two can be compared is
// installation: after hooks land successfully, stat the bd PATH selects and
// print one stderr diagnostic when it is another file or none. The candidate
// is never executed, and the diagnostic never fires from a test binary or
// when this process's own identity cannot be resolved.

// bdPathSelection classifies how the bd PATH selects relates to the running
// bd.
type bdPathSelection int

const (
	// bdPathUnknown: this process's identity is unavailable, or a candidate
	// exists but cannot be compared — stay silent rather than speculate.
	bdPathUnknown bdPathSelection = iota
	// bdPathSame: PATH selects this very file (directly, via a symlink or via
	// a hardlink) — nothing to say.
	bdPathSame
	// bdPathDifferent: PATH selects another bd file.
	bdPathDifferent
	// bdPathMissing: PATH selects no bd at all.
	bdPathMissing
)

// classifyBdPathSelection compares selfPath (the running bd, canonicalized)
// with pathBD (the bd exec.LookPath selected; "" when PATH holds none). It
// only stats the two files and never executes either: a LookPath result that
// carries exec.ErrDot is a nonempty (relative) path and counts as found,
// resolved against the process working directory exactly as a bare `bd`
// invocation would resolve it.
func classifyBdPathSelection(selfPath, pathBD string) bdPathSelection {
	if pathBD == "" {
		return bdPathMissing
	}
	selfInfo, err := os.Stat(selfPath)
	if err != nil {
		return bdPathUnknown
	}
	pathInfo, err := os.Stat(pathBD)
	if err != nil {
		return bdPathUnknown
	}
	if os.SameFile(selfInfo, pathInfo) {
		return bdPathSame
	}
	return bdPathDifferent
}

// bdPathSelectionWarning returns the install-time stderr diagnostic for one
// selection outcome, or "" when the install should stay silent: same file
// (including a symlink or hardlink to self), a test binary, or a self
// identity that could not be resolved. The message names both paths when
// PATH selects another bd and just this bd when it selects none, and is one
// diagnostic — a single beads: warning followed by its explanation lines.
func bdPathSelectionWarning(selfPath string, selfErr error, inTestHarness bool, pathBD string) string {
	if inTestHarness || selfErr != nil {
		return ""
	}
	if isTestBinaryPath(selfPath) {
		return ""
	}
	switch classifyBdPathSelection(selfPath, pathBD) {
	case bdPathMissing:
		return fmt.Sprintf(
			"beads: warning: the installed hooks resolve 'bd' from PATH, but PATH has no bd now; this bd ran from %s.\n"+
				"beads: git runs hooks in a non-interactive shell — put this bd's directory on PATH, or commits silently skip the hooks.\n",
			selfPath)
	case bdPathDifferent:
		return fmt.Sprintf(
			"beads: warning: PATH selects bd at %s, but these hooks were installed by %s.\n"+
				"beads: at commit time the hooks run the PATH copy — a stale bd there fails to open databases the current bd wrote (GH#5922).\n"+
				"beads: remove the stale install or correct PATH; see docs/reference/troubleshooting.md.\n",
			pathBD, selfPath)
	default:
		return ""
	}
}

// warnBdPathSelection prints the install-time PATH diagnostic on stderr. It
// is called once per user-facing install boundary — a successful explicit
// `bd hooks install` and a non-quiet hook install or update during `bd init`
// — never after a quiet, failed or skipped install, where there is no user
// moment to warn in. Warnings go to stderr so stdout, JSON included, stays
// valid for consumers.
func warnBdPathSelection() {
	selfPath, selfErr := resolveSelfExecutable()
	pathBD, _ := exec.LookPath("bd")
	if msg := bdPathSelectionWarning(selfPath, selfErr, testing.Testing(), pathBD); msg != "" {
		fmt.Fprint(os.Stderr, msg)
	}
}
