package doltserver

import (
	"os"
	"path/filepath"
	"runtime"
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

// selectOrphanTestServers returns the candidates that are safe to reap as
// leaked test debris, each carrying the working directory it was serving. A
// candidate qualifies only when its cmdline names a dolt sql-server AND
// either:
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
func selectOrphanTestServers(candidates []serverCandidate, suiteRoots, tempRoots []string) []SweptServer {
	return selectServers(candidates, func(c serverCandidate) bool {
		if underAnyRoot(c.cwd, suiteRoots) {
			return true
		}
		return c.cwdDeleted && underAnyRoot(c.cwd, tempRoots)
	})
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
		if !isDoltServerCmdline(c.cmdline) || c.cwd == "" {
			continue
		}
		if want(c) {
			selected = append(selected, SweptServer{PID: c.pid, Cwd: c.cwd})
		}
	}
	return selected
}

// tempDirRoots is the set of directories under which a deleted working
// directory is credible evidence of leaked TEST debris rather than a moved
// production workspace: the process temp dir (honoring TMPDIR, which the
// suites pin to their own root), GOTMPDIR, and /tmp, which os.MkdirTemp uses
// when TMPDIR is unset and which several suites hardcode.
//
// GOTMPDIR is where testing.T.TempDir roots every test's directories when it
// is set (Go 1.26+), independently of TMPDIR, while os.TempDir() reads TMPDIR
// alone. A host that EXPORTS GOTMPDIR as a disk path and leaves TMPDIR unset
// puts every t.TempDir() — and every test server's data dir under it —
// outside both os.TempDir() and /tmp (TestTempDirRootsCoverGOTMPDIR).
//
// Exported is the operative word: `go env -w GOTMPDIR` writes the go env
// config file, which cmd/go consumes for its own build work dir and does not
// put into the test binary's environment. It therefore reaches neither
// testing.T.TempDir nor this function, both of which read the PROCESS
// environment — which is also why os.Getenv below is the right lookup: it is
// byte-for-byte the one testing.T.TempDir performs.
//
// Roots too broad to be evidence of anything are dropped — see
// isCredibleTempRoot. TMPDIR and GOTMPDIR are environment variables, so "/"
// or a home directory can land here, and a root that broad would restore
// exactly the unbounded deleted-cwd arm this bound exists to remove.
func tempDirRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	var roots []string
	for _, root := range canonicalRoots([]string{os.TempDir(), os.Getenv("GOTMPDIR"), "/tmp"}) {
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
// can never read or write the runner's real dotfiles, and several suites pin
// HOME to a t.TempDir() for the same reason. Such a home lives under a fixed
// system temp location (isSandboxHome), and without this carve-out it would
// disqualify that location itself, leaving tempDirRoots empty and the
// deleted-cwd arm silently disabled on exactly the boxes whose killed runs it
// exists to clean up after. A root that merely contains a sandbox home stays
// credible; a root that IS the home does not, whatever the home looks like.
//
// The carve-out is itself bounded by isSharedTempRoot: a sandbox home rescues
// a root NESTED inside a fixed temp location, never one of those shared
// locations itself. Otherwise a bare TMPDIR=/var/tmp — where mktemp -d puts
// the sandbox home directly under the shared root — would hand back all of
// /var/tmp, and with it every other user's workspace there.
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
		if !isUnderDir(h, cleaned) {
			continue
		}
		if !isSandboxHome(h) || isSharedTempRoot(cleaned, runtime.GOOS) {
			return false
		}
	}
	return true
}

// isSandboxHome reports whether home lives inside a FIXED system temp
// location — the shape a test harness's throwaway HOME takes. It is judged
// against hardcoded locations, never os.TempDir(), so TMPDIR cannot vote on
// its own credibility: TMPDIR=/home with HOME=/home/runner must still read as
// a real home under an overbroad root (TestTempDirRootsRejectsOverbroadTMPDIR).
func isSandboxHome(home string) bool {
	return isUnderFixedTempRoots(home, runtime.GOOS)
}

// isUnderFixedTempRoots reports whether path lies under one of the temp
// locations goos puts at a FIXED, non-configurable place. goos is a parameter
// rather than a read of runtime.GOOS so the platform table is testable from
// any platform (TestFixedTempRootsAreCredibleSandboxHomes).
//
// The list is deliberately hardcoded — see isSandboxHome — and canonicalRoots
// expands each entry so macOS's /private/var/folders form matches too.
func isUnderFixedTempRoots(path, goos string) bool {
	return underAnyRoot(path, canonicalRoots(fixedTempRoots(goos)))
}

// fixedTempRoots is the platform table behind isUnderFixedTempRoots.
//
// /tmp is everywhere: it is os.MkdirTemp's fallback when TMPDIR is unset, and
// CI harnesses (scripts/ci/lib/test-env.sh) mktemp -d their sandbox HOME under
// it. macOS additionally gives every user a per-user temp tree under
// /var/folders and points TMPDIR at it, so a suite that pins HOME to a
// t.TempDir() (cmd/bd/test_repo_beads_guard_test.go, internal/beads/testmain_test.go)
// puts HOME under /var/folders/xx/yy/T — which without this entry disqualified
// os.TempDir() itself and left the deleted-cwd arm inert on every Mac
// (wy-j2zc8q).
//
// /var/tmp is the same story on a Linux gate host whose TMPDIR points at a
// disk-backed path there instead of a tmpfs /tmp: test-env.sh mktemp -ds the
// sandbox HOME under WHATEVER TMPDIR is, not a hardcoded /tmp. Without this
// entry, isSandboxHome only recognized /tmp on Linux, so that sandbox HOME
// read as a real home containing os.TempDir(), isCredibleTempRoot
// disqualified os.TempDir() itself, and tempDirRoots() collapsed to [/tmp] —
// stranding every t.TempDir() actually rooted under the /var/tmp-based TMPDIR
// (TestTempDirRootsBoundTheOrphanArm, be-n9ile / be-35sef).
//
// The darwin row spells out the symlink-RESOLVED forms ("/private/tmp",
// "/private/var/folders", "/private/var/tmp") literally instead of leaving
// them to canonicalRoots. canonicalRoots resolves with filepath.EvalSymlinks,
// which reads the HOST's filesystem: on a Mac it turns /tmp, /var/folders,
// and /var/tmp into their /private/… targets, and on a Linux runner it adds
// nothing at all. But this table is a claim ABOUT darwin that any platform
// may be asked to judge — the platform row is pinned from Linux CI by
// TestSandboxHomeUnderPerUserTempRoot, and a Mac's lsof reports cwds in the
// /private/… form — so the answer must not depend on where the judging
// happens. canonicalRoots dedups, so on a real Mac these literals cost
// nothing: they are exactly what it would have added.
//
// This table only feeds isSandboxHome's home-containment carve-out inside
// isCredibleTempRoot; it is never unioned into tempDirRoots()'s own output
// (canonicalRoots([os.TempDir(), "/tmp"])). That by itself does NOT bound the
// entry, because os.TempDir() can BE /var/tmp: under a bare TMPDIR=/var/tmp,
// mktemp -d puts the sandbox HOME directly under the shared root, and the
// carve-out would then spare /var/tmp itself as a sweep root — putting every
// other user's deleted-cwd dolt server on the box in range of the kill arm.
// isSharedTempRoot is what closes that: a sandbox HOME may rescue a root
// nested INSIDE an entry here (/var/tmp/beads-gate-probe, the shape
// test-env.sh builds), never the entry itself. So adding /var/tmp widens the
// orphan arm by nothing — a bare TMPDIR=/var/tmp is judged exactly as it was
// before this row existed (TestSandboxHomeUnderVarTmpTMPDIR).
func fixedTempRoots(goos string) []string {
	roots := []string{"/tmp", "/var/tmp"}
	if goos == "darwin" {
		roots = append(roots, "/private/tmp", "/var/folders", "/private/var/folders", "/private/var/tmp")
	}
	return roots
}

// isSharedTempRoot reports whether root IS one of the temp locations every
// user on the box shares, rather than a path nested inside one. goos is a
// parameter for the same reason as isUnderFixedTempRoots: the platform table
// must answer identically wherever it is judged.
//
// This is the bound on isCredibleTempRoot's sandbox-home carve-out. That
// carve-out recognizes a throwaway HOME by the fixed temp location it sits
// under, so without this check it would spare the whole shared location as a
// sweep root whenever TMPDIR pointed AT it instead of inside it — and the
// deleted-cwd arm reaps what it matches (TestSandboxHomeUnderVarTmpTMPDIR).
func isSharedTempRoot(root, goos string) bool {
	cleaned := filepath.Clean(root)
	for _, shared := range canonicalRoots(sharedTempRoots(goos)) {
		if filepath.Clean(shared) == cleaned {
			return true
		}
	}
	return false
}

// sharedTempRoots is fixedTempRoots minus the /tmp family: the entries that
// may vouch for a sandbox HOME but must never become a sweep root on its
// account. Derived rather than spelled out so a future row is bounded by
// default — opting one out has to be a deliberate edit here.
//
// /tmp is the deliberate opt-out. tempDirRoots() hardcodes it into the
// candidate list (canonicalRoots([os.TempDir(), "/tmp"])), so the design
// already credits it unconditionally and excluding it here widens nothing;
// including it would instead re-break the case the carve-out exists for,
// since the CI shape puts HOME directly under /tmp
// (TestTempDirRootsRejectsOverbroadTMPDIR/"sandbox HOME under /tmp keeps
// /tmp"). darwin's /private/tmp is that same directory under its resolved
// name.
func sharedTempRoots(goos string) []string {
	var shared []string
	for _, root := range fixedTempRoots(goos) {
		if root == "/tmp" || root == "/private/tmp" {
			continue
		}
		shared = append(shared, root)
	}
	return shared
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
