package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/steveyegge/beads/internal/ui"
)

// hookSubprocessBinary names the bd the git-hook lanes run for their `bd
// export` (pre-commit) and `bd import` (post-merge, post-checkout) children.
// A var so tests can see which binary a lane would launch.
var hookSubprocessBinary = resolveHookSubprocessBinary

// resolveHookSubprocessBinary is the production resolver for
// hookSubprocessBinary.
func resolveHookSubprocessBinary() string {
	exe, err := os.Executable()
	return pickHookSubprocessBinary(exe, err, isTestExecutable)
}

// pickHookSubprocessBinary returns the running bd, so a hook's child is the
// same binary as the hook that started it. A bare "bd" let the child be
// whatever PATH selects: under git's non-interactive shell, or when the hook
// was started by absolute path, that can be another, older install, which
// then fails to open (or quietly rewrites) a database this bd wrote (GH#5922).
//
// Symlinks are resolved so every re-exec site agrees on one path; a failed
// resolution keeps the unresolved path rather than falling back to PATH,
// which would bring the stale selection back (resolvePrimeExecutable makes
// the same trade-off). The bare name, looked up on PATH, is the fallback only
// when the running binary cannot be discovered at all, and inside a test
// binary: re-executing a test binary as bd would launch the suite again, and
// hook tests put a stub bd on PATH for the child to find.
func pickHookSubprocessBinary(exe string, exeErr error, isTest func(string) bool) string {
	if exeErr != nil || exe == "" {
		return "bd"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if isTest(exe) {
		return "bd"
	}
	return exe
}

// warnHookPATHBinary prints hookPATHBinaryWarning's diagnostic for the
// running bd to stderr. Call it only after hooks were installed or updated,
// on a path that is allowed to talk (not --quiet); stdout, including --json
// output, is left alone. Nothing is printed when the running binary cannot be
// identified, or is a test binary.
func warnHookPATHBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if isTestExecutable(exe) {
		return
	}
	fmt.Fprint(os.Stderr, hookPATHBinaryWarning(exe, exec.LookPath))
}

// hookPATHBinaryWarning returns the diagnostic for hooks installed by self,
// or "" when there is nothing to say. Installed hooks start bd by name
// (`command -v bd` in the hook shell), so they run whatever bd PATH selects,
// not the bd that installed them: when that is another file the hooks may
// run an older install, and when there is none they skip their beads work.
// The bd PATH selects here is the installing shell's; git, an IDE or a GUI
// client may start hooks with another PATH, which no check at install time
// can see.
//
// Only file identity is compared (so a symlink or hard link to self is the
// same bd), and no candidate is ever executed. Two files may hold the same
// version, so the text names the paths without claiming the other bd is
// older. A match exec.LookPath reports as exec.ErrDot (found through a
// relative PATH entry) still counts: the hook shell would run it. When self
// itself cannot be stat'ed there is no identity to compare, so nothing is
// said.
func hookPATHBinaryWarning(self string, lookPath func(string) (string, error)) string {
	selfInfo, err := os.Stat(self)
	if err != nil {
		return ""
	}
	const check = "  Check what git hooks will run: sh -c 'command -v bd; bd version'\n"
	found, err := lookPath("bd")
	if err != nil && (!errors.Is(err, exec.ErrDot) || found == "") {
		return fmt.Sprintf("%s Git hooks run bd from PATH, but PATH has no bd: the hooks will skip their beads work.\n"+
			"  This bd: %s\n"+
			"  Put its directory on the PATH git uses.\n"+check, ui.RenderWarn("⚠"), self)
	}
	if foundInfo, err := os.Stat(found); err == nil && os.SameFile(selfInfo, foundInfo) {
		return ""
	}
	return fmt.Sprintf("%s Git hooks run the bd that PATH selects, which is not this bd:\n"+
		"  PATH selects: %s\n"+
		"  This bd:      %s\n"+
		"  An older bd there can fail to open this database, and commits then miss their issue export.\n"+
		"  Upgrade or remove that bd, or put this one first on the PATH git uses.\n"+check, ui.RenderWarn("⚠"), found, self)
}
