package doltserver

import (
	"path/filepath"
	"strconv"
	"strings"
)

// serverCandidate is a running process that looked like a `dolt sql-server`
// (isDoltServerArgv), along with enough identity data to judge whether it is
// leaked test debris.
type serverCandidate struct {
	pid int
	// argv is the process's command line split into arguments. On Darwin it
	// is approximate: ps prints the arguments space-joined, so they are
	// recovered with strings.Fields (see isDoltServerCmdline).
	argv []string
	// cwd is the process's resolved working directory. Empty if unknown.
	cwd string
	// cwdDeleted is true when cwd names a directory that no longer exists
	// (e.g. Linux's /proc/<pid>/cwd symlink grew a " (deleted)" suffix
	// because something rm -rf'd the directory out from under the process).
	cwdDeleted bool
}

// SweptServer names one leaked `dolt sql-server` a sweep selected: the PID it
// signaled and the working directory that process was serving.
//
// The cwd is the whole reason this type exists. A sweep report of bare PIDs
// ("swept 1 orphaned test dolt sql-server process(es): [21881]") says a
// fixture leaked but not WHICH one, and the PID is gone by the time anyone
// reads the log — so a CI failure on a package with hundreds of tests left
// the reader nothing to grep for. A leaked server's cwd is the .beads/dolt
// directory under the suite's own temp tree, and Go names those after the
// test that made them (a t.TempDir() is <root>/TestSomeName1234/001), so the
// directory identifies the leaking test directly.
type SweptServer struct {
	// PID is the process that was signaled.
	PID int
	// Cwd is the working directory the process was serving. Never empty for
	// a selected server: selectServers skips candidates with no known cwd,
	// because unknown is not provably debris.
	Cwd string
}

// String renders a swept server as "<pid> cwd=<dir>", which is what the
// sweep's stderr lines print for each entry (fmt applies this to every
// element of a []SweptServer formatted with %v).
func (s SweptServer) String() string {
	if s.Cwd == "" {
		return strconv.Itoa(s.PID)
	}
	return strconv.Itoa(s.PID) + " cwd=" + s.Cwd
}

// selectServersUnderRoots returns the candidates whose working directory sits
// under one of roots, deleted or not, and nothing else. It is the only
// selection the sweeps use, so a sweep can never reach a process outside the
// caller's own trees.
//
// SweepDeadSuiteRoots uses it because that sweep runs at suite START, while
// sibling packages (go test -p N) are mid-run: whatever it reaps must be
// provably inside the one dead root it is cleaning up, never merely
// "somewhere temporary".
func selectServersUnderRoots(candidates []serverCandidate, roots []string) []SweptServer {
	return selectServers(candidates, func(c serverCandidate) bool {
		return underAnyRoot(c.cwd, roots)
	})
}

// selectServers applies want to every candidate that is a dolt sql-server
// with a known working directory, returning the survivors as SweptServers. A
// candidate whose cwd could not be resolved is always skipped: unknown is not
// provably debris — which is also why every SweptServer it returns has a
// non-empty Cwd for the sweep's report to name.
func selectServers(candidates []serverCandidate, want func(serverCandidate) bool) []SweptServer {
	var selected []SweptServer
	for _, c := range candidates {
		if !isDoltServerArgv(c.argv) || c.cwd == "" {
			continue
		}
		if want(c) {
			selected = append(selected, SweptServer{PID: c.pid, Cwd: c.cwd})
		}
	}
	return selected
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

// isDoltServerArgv reports whether argv looks like a dolt sql-server
// invocation: the executable's basename is exactly "dolt" and some later
// argument is exactly "sql-server". Any later argument counts, not just
// argv[1], because debug mode inserts flags between the binary name and the
// subcommand (e.g. `dolt --prof cpu --prof-path … sql-server …`).
//
// This is the identity gate in front of SIGTERM/SIGKILL, so it is strict on
// purpose (gastownhall/beads#6186): a process that merely mentions both words
// (`tail -f dolt-sql-server.log`, a shell whose body or arguments name them,
// `dolt status`) is not a server, and neither is a wrapper, a renamed binary,
// `dolt.exe` or a prefix match. It is a lexical check, not a parser of Dolt's
// grammar: a positional `sql-server` (`dolt checkout sql-server`) still
// passes, and argv is whatever the process presents, not proof of which
// executable it runs.
//
// It deliberately diverges from parseDoltProcessPIDs in doltserver_unix.go,
// which feeds production server management and keeps its looser ordered
// substring match; every argv accepted here is also accepted there
// (TestSweepPredicateIsSubsetOfProductionMatch), never the reverse.
func isDoltServerArgv(argv []string) bool {
	if len(argv) < 2 || filepath.Base(argv[0]) != "dolt" {
		return false
	}
	for _, arg := range argv[1:] {
		if arg == "sql-server" {
			return true
		}
	}
	return false
}

// isDoltServerCmdline applies isDoltServerArgv to a command line that is
// already space-joined, as Darwin's ps prints it. strings.Fields only
// approximates the original arguments: an executable path containing a space
// splits apart (and is rejected), and a quoted argument such as the message in
// `dolt commit -m "restart sql-server"` cannot be told from a separate
// `sql-server` argument. Linux reads exact argument boundaries from /proc.
func isDoltServerCmdline(cmdline string) bool {
	return isDoltServerArgv(strings.Fields(cmdline))
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
// uses this path because it has no /proc filesystem. The command column is
// split with strings.Fields, so its argv is approximate (isDoltServerCmdline).
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
		argv := strings.Fields(cmdline)
		if !isDoltServerArgv(argv) {
			continue
		}

		cwd, deleted, ok := cwdForPID(pid)
		if !ok {
			continue
		}
		candidates = append(candidates, serverCandidate{
			pid:        pid,
			argv:       argv,
			cwd:        cwd,
			cwdDeleted: deleted,
		})
	}
	return candidates
}
