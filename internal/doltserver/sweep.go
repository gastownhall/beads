package doltserver

import (
	"strings"
)

// serverCandidate is a running process that looked like a `dolt sql-server`
// from a coarse filter (cmdline substring match), along with enough
// identity data to judge whether it is leaked test debris.
type serverCandidate struct {
	pid int
	// cmdline is the process's command line, space-joined.
	cmdline string
	// cwd is the process's resolved working directory. Empty if unknown.
	cwd string
	// cwdDeleted is true when cwd names a directory that no longer exists
	// (e.g. Linux's /proc/<pid>/cwd symlink grew a " (deleted)" suffix
	// because something rm -rf'd the directory out from under the process).
	cwdDeleted bool
}

// selectOrphanTestServerPIDs returns the PIDs of candidates that are safe to
// reap as leaked test debris. A candidate qualifies only when its cmdline
// names a dolt sql-server AND either:
//
//   - its working directory has been deleted (the temp dir it was serving
//     no longer exists — this is the signature of a SIGKILLed test run
//     whose t.TempDir() cleanup ran on top of a still-live server), or
//   - its working directory sits under one of suiteRoots.
//
// suiteRoots MUST be directories owned by the calling test suite alone
// (e.g. that suite's own testTempRoot) — never a shared/global temp dir
// such as os.TempDir(). A live (non-deleted-cwd) server is only reaped when
// its data dir is nested under a root the caller vouches for as its own;
// otherwise a parallel test run (scripts/test.sh -p N) would see every
// *other* suite's still-live server as debris, since virtually all suites'
// data dirs live somewhere under os.TempDir() too. Passing a global root
// here would turn this safety net into a cross-suite server killer.
//
// This is intentionally conservative in the "never kill production" sense:
// a real shared server's data directory is a persistent, non-temp path that
// still exists and is never one of a test suite's own scoped roots, so it
// matches neither condition and is left alone.
func selectOrphanTestServerPIDs(candidates []serverCandidate, suiteRoots []string) []int {
	var pids []int
	for _, c := range candidates {
		if !isDoltServerCmdline(c.cmdline) {
			continue
		}
		if c.cwdDeleted {
			pids = append(pids, c.pid)
			continue
		}
		if c.cwd == "" {
			continue
		}
		if underAnyRoot(c.cwd, suiteRoots) {
			pids = append(pids, c.pid)
		}
	}
	return pids
}

// isDoltServerCmdline reports whether cmdline looks like a dolt sql-server
// invocation. Mirrors the substring check in listDoltProcessPIDs (both
// "dolt" and "sql-server" must appear) rather than an exact match, since
// debug mode inserts flags between the binary name and the subcommand
// (e.g. `dolt --prof cpu --prof-path … sql-server …`).
func isDoltServerCmdline(cmdline string) bool {
	return strings.Contains(cmdline, "dolt") && strings.Contains(cmdline, "sql-server")
}

// underAnyRoot reports whether dir is equal to, or nested under, any of
// roots. Empty roots are ignored so callers can pass optional extras
// without filtering first.
func underAnyRoot(dir string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		if isUnderDir(dir, root) {
			return true
		}
	}
	return false
}

// isUnderDir reports whether dir is root itself or a descendant of root.
// Both paths are compared as given (callers are expected to pass already
// resolved/absolute paths); this only does the string-prefix-with-boundary
// check, no filesystem access.
func isUnderDir(dir, root string) bool {
	root = strings.TrimRight(root, "/")
	if root == "" {
		return false
	}
	if dir == root {
		return true
	}
	return strings.HasPrefix(dir, root+"/")
}
