package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
// whatever PATH selects: when the hook's bd was started by absolute path, an
// alias or a hook manager, that can be another, older install, which then
// fails to open (or quietly rewrites) a database this bd wrote (GH#5922).
// bd's own hooks start their bd from PATH, so there parent and child already
// agree; hookPATHBinaryWarning covers that selection.
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
// same bd), and no candidate is ever executed. A PATH bd that resolves,
// through its symlinks, to a file beside self with self's name and another
// extension is a launcher installed with self, not another install (see
// installedBeside): npm links <prefix>/bin/bd and
// node_modules/.bin/bd to bin/bd.js, a Node launcher that starts the native bd
// next to it, so the hooks run self and nothing is said. Any other file may
// still be a wrapper or shim that runs self, or a copy of the same version,
// so the text names both paths and self's version and leaves the verdict to
// the version check instead of claiming the other bd is older. A match
// exec.LookPath reports as exec.ErrDot (found through a relative PATH entry)
// still counts: the hook shell would run it. When self itself cannot be
// stat'ed there is no identity to compare, so nothing is said.
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
	if installedBeside(found, self) {
		return ""
	}
	return fmt.Sprintf("%s Git hooks run the bd that PATH selects, a different file from this bd:\n"+
		"  PATH selects: %s\n"+
		"  This bd:      %s (version %s)\n"+check+
		"  If it shows an older version, hooks can fail to open this database and commits miss their issue export:\n"+
		"  upgrade or remove that bd, or put this one first on the PATH git uses. A wrapper that runs this bd is fine.\n",
		ui.RenderWarn("⚠"), found, self, Version)
}

// installedBeside reports whether found, resolved through its symlinks, is a
// launcher installed with self: a file in self's directory with self's name
// and another extension, such as npm's bin/bd.js beside bin/bd. Any other file
// there, an older bd-1.0.5 or a plain bd beside a differently named self, is
// another install and still draws the warning.
func installedBeside(found, self string) bool {
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil {
		return false
	}
	if resolved, err = filepath.Abs(resolved); err != nil {
		return false
	}
	if filepath.Dir(resolved) != filepath.Dir(self) {
		return false
	}
	stem := func(p string) string { return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)) }
	return stem(resolved) == stem(self) && filepath.Ext(resolved) != filepath.Ext(self)
}
