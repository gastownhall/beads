package doltserver

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// serverCandidate is a running process that looked like a `dolt sql-server`
// from the argv identity gate, along with enough identity data to judge
// whether it is leaked test debris.
type serverCandidate struct {
	pid int
	// argv is the process's argument vector. On linux it is the actual
	// NUL-delimited argv from /proc (procArgv); on darwin it is the ps
	// command column split on whitespace, which only approximates the
	// arguments — see gatherPSCandidates.
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
// under one of roots, and nothing else. It is the strictly
// root-scoped selection: no deleted-cwd arm, so it can never reach a process
// outside the caller's own trees.
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

// isDoltServerCmdline reports whether cmdline looks like a dolt sql-server
// invocation, for the flattened command column darwin's ps revalidation
// probe hands the sweep. The words are split into approximate arguments,
// because that column carries no argument boundaries — linux reads the real
// NUL-delimited argv via procArgv instead. See isDoltServerArgv, which
// holds the actual rule.
func isDoltServerCmdline(cmdline string) bool {
	return isDoltServerArgv(strings.Fields(cmdline))
}

// isDoltServerArgv reports whether argv looks like a `dolt sql-server`
// invocation: at least two elements, an argv[0] whose basename is exactly
// "dolt", and a later argument exactly "sql-server" — searched across every
// later element, so global flags and profiling options between the binary and
// the subcommand (e.g. `dolt --prof cpu … sql-server …`) keep matching.
//
// This is the sweep's identity gate, consulted before every signal, and it is
// deliberately stricter than the substring listing in parseDoltProcessPIDs
// (doltserver_unix.go): the sweep signals on identity alone, so a
// `tail -f dolt-sql-server.log` under a suite root must not read as a server
// (#6186). `dolt.exe`, prefix matches, shell wrappers and renamed binaries
// are not accepted. It is a lexical predicate — not a parser of dolt's flag
// grammar, and not executable authentication.
func isDoltServerArgv(argv []string) bool {
	if len(argv) < 2 || filepath.Base(argv[0]) != "dolt" {
		return false
	}
	return slices.Contains(argv[1:], "sql-server")
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
// The command column carries no argument boundaries, so its words are split
// into approximate arguments with strings.Fields: a quoted argument
// containing spaces ("dolt commit -m 'restart sql-server'") cannot be
// recovered, and an executable path containing spaces splits into several
// tokens. That is the platform's limit, documented here rather than hidden
// in the identity gate.
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
		argv := strings.Fields(strings.TrimSpace(cmdline))
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
