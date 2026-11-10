package main

import (
	"os"
	"path/filepath"
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
