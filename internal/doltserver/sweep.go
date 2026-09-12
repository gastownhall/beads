package doltserver

import (
	"os"
	"path/filepath"
	"strconv"
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
//   - its working directory sits under one of suiteRoots, or
//   - its working directory has been deleted AND the path it used to name
//     sits under one of tempRoots.
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
// tempRoots (see tempDirRoots) bounds the deleted-cwd arm to throwaway
// directories. A deleted working directory is a strong leak signal — a
// t.TempDir() cleanup ran on top of a still-live detached server — but it is
// NOT by itself proof of a TEST server: a production server is spawned with
// cmd.Dir = <workspace>/.beads/dolt, so a developer who moved or deleted that
// workspace, or whose external volume was unmounted, would have their live
// server reaped by any test run on the box. Requiring the deleted path to
// have been under a temp dir keeps the arm pointed at test debris only.
//
// This is intentionally conservative in the "never kill production" sense:
// a real shared server's data directory is a persistent, non-temp path, so it
// is neither under a suite's scoped roots nor — deleted or not — under a temp
// root, and matches neither condition.
func selectOrphanTestServerPIDs(candidates []serverCandidate, suiteRoots, tempRoots []string) []int {
	return selectCandidatePIDs(candidates, func(c serverCandidate) bool {
		if underAnyRoot(c.cwd, suiteRoots) {
			return true
		}
		return c.cwdDeleted && underAnyRoot(c.cwd, tempRoots)
	})
}

// selectServersUnderRoots returns the PIDs of candidates whose working
// directory sits under one of roots, and nothing else. It is the strictly
// root-scoped selection: no deleted-cwd arm, so it can never reach a process
// outside the caller's own trees.
//
// SweepDeadSuiteRoots uses it because that sweep runs at suite START, while
// sibling packages (go test -p N) are mid-run: whatever it reaps must be
// provably inside the one dead root it is cleaning up, never merely
// "somewhere temporary".
func selectServersUnderRoots(candidates []serverCandidate, roots []string) []int {
	return selectCandidatePIDs(candidates, func(c serverCandidate) bool {
		return underAnyRoot(c.cwd, roots)
	})
}

// selectCandidatePIDs applies want to every candidate that is a dolt
// sql-server with a known working directory. A candidate whose cwd could not
// be resolved is always skipped: unknown is not provably debris.
func selectCandidatePIDs(candidates []serverCandidate, want func(serverCandidate) bool) []int {
	var pids []int
	for _, c := range candidates {
		if !isDoltServerCmdline(c.cmdline) || c.cwd == "" {
			continue
		}
		if want(c) {
			pids = append(pids, c.pid)
		}
	}
	return pids
}

// tempDirRoots is the set of directories under which a deleted working
// directory is credible evidence of leaked TEST debris rather than a moved
// production workspace: the process temp dir (honoring TMPDIR, which the
// suites pin to their own root) and /tmp, which os.MkdirTemp uses when TMPDIR
// is unset and which several suites hardcode.
//
// Roots too broad to be evidence of anything are dropped — see
// isCredibleTempRoot. TMPDIR is an environment variable, so "/" or a home
// directory can land here, and a root that broad would restore exactly the
// unbounded deleted-cwd arm this bound exists to remove.
func tempDirRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	var roots []string
	for _, root := range canonicalRoots([]string{os.TempDir(), "/tmp"}) {
		if !isCredibleTempRoot(root, home) {
			continue
		}
		roots = append(roots, root)
	}
	return roots
}

// isCredibleTempRoot reports whether root is narrow enough to bound the
// deleted-cwd arm. It rejects the filesystem root, a relative or empty path,
// and any directory that IS the user's home or contains it — "/", "/Users",
// "/home", $HOME itself. Everything a real temp dir looks like (/tmp,
// /private/tmp, /var/folders/xx/yy/T, a pinned suite root) passes.
//
// The home test doubles as the breadth test: a directory holding the user's
// home holds their workspaces too, so a deleted .beads/dolt under it would
// again look like test debris.
//
// The one home that anchors no workspaces is a SANDBOX home: CI harnesses
// (scripts/ci/lib/test-env.sh) export HOME under a mktemp -d root so a test
// can never read or write the runner's real dotfiles. That home lives under
// /tmp, and without this carve-out it would disqualify /tmp itself, leaving
// tempDirRoots empty and the deleted-cwd arm silently disabled on exactly the
// boxes whose killed runs it exists to clean up after. A root that merely
// contains a sandbox home stays credible; a root that IS the home does not,
// whatever the home looks like.
func isCredibleTempRoot(root, home string) bool {
	cleaned := filepath.Clean(root)
	if cleaned == "" || cleaned == "." || cleaned == string(filepath.Separator) {
		return false
	}
	if !filepath.IsAbs(cleaned) {
		return false
	}
	if home == "" {
		return true
	}
	for _, h := range canonicalRoots([]string{home}) {
		h = filepath.Clean(h)
		if h == cleaned {
			return false
		}
		if isUnderDir(h, cleaned) && !isSandboxHome(h) {
			return false
		}
	}
	return true
}

// isSandboxHome reports whether home lives inside the fixed /tmp fallback —
// the shape a test harness's throwaway HOME takes. It is judged against the
// hardcoded fallback, never os.TempDir(), so TMPDIR cannot vote on its own
// credibility: TMPDIR=/home with HOME=/home/runner must still read as a real
// home under an overbroad root.
func isSandboxHome(home string) bool {
	return underAnyRoot(home, canonicalRoots([]string{"/tmp"}))
}

// canonicalRoots expands each non-empty root into every form a process's
// working directory may be reported in: the path as given and, when it
// differs, its symlink-resolved form. macOS is why — os.MkdirTemp hands back
// /var/folders/… while lsof reports the /private/var/folders/… that symlink
// points at — but /tmp is a symlink to /private/tmp there too, and on Linux
// /tmp can be a symlink as well. Roots that cannot be resolved are kept
// as-is; duplicates are dropped.
func canonicalRoots(roots []string) []string {
	var out []string
	seen := make(map[string]bool, len(roots)*2)
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		add(root)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			add(resolved)
		}
	}
	return out
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

// gatherPSCandidates parses the output of `ps -axo pid=,command=` and
// resolves the working directory of each dolt sql-server candidate. Darwin
// uses this path because it has no /proc filesystem.
//
// cwdForPID returns the resolved cwd, whether that cwd has been deleted, and
// whether it could be determined. Keeping the command execution outside this
// parser makes the safety-critical selection path deterministic to test.
func gatherPSCandidates(psOutput []byte, cwdForPID func(int) (string, bool, bool)) []serverCandidate {
	var candidates []serverCandidate
	for _, line := range strings.Split(string(psOutput), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		pidText, cmdline, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			continue
		}
		cmdline = strings.TrimSpace(cmdline)
		if !isDoltServerCmdline(cmdline) {
			continue
		}

		cwd, deleted, ok := cwdForPID(pid)
		if !ok {
			continue
		}
		candidates = append(candidates, serverCandidate{
			pid:        pid,
			cmdline:    cmdline,
			cwd:        cwd,
			cwdDeleted: deleted,
		})
	}
	return candidates
}
