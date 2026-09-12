//go:build darwin

package doltserver

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// SweepOrphanedTestServers reaps `dolt sql-server` processes that are
// provably leaked test debris: their working directory has been deleted, or
// sits under one of suiteTempRoots. On Darwin, process candidates come from
// ps and their working directories from lsof because /proc is unavailable.
//
// suiteTempRoots MUST be directories owned by the calling suite alone, never
// a shared/global temp directory. This is best-effort: process-listing errors
// and candidates whose cwd cannot be resolved are ignored.
//
// Returns the PIDs it sent a kill signal to.
func SweepOrphanedTestServers(suiteTempRoots ...string) []int {
	candidates := gatherDoltServerCandidates()
	pids := selectOrphanTestServerPIDs(candidates, canonicalRoots(suiteTempRoots), tempDirRoots())
	return reapServerPIDs(pids, isDoltServerProcess)
}

// sweepServersUnderRoots reaps only the dolt sql-servers whose working
// directory sits under one of suiteTempRoots. It is SweepOrphanedTestServers
// without the deleted-cwd arm, for callers that must not reach outside the
// trees they name — see selectServersUnderRoots.
//
// Returns the PIDs it sent a kill signal to.
func sweepServersUnderRoots(suiteTempRoots ...string) []int {
	candidates := gatherDoltServerCandidates()
	pids := selectServersUnderRoots(candidates, canonicalRoots(suiteTempRoots))
	return reapServerPIDs(pids, isDoltServerProcess)
}

func gatherDoltServerCandidates() []serverCandidate {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	return gatherPSCandidates(out, readDarwinCwd)
}

func isDoltServerProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return isDoltServerCmdline(strings.TrimSpace(string(out)))
}

// statFunc matches os.Stat. parseDarwinCwd takes one so its
// deleted-directory probe can be exercised without a live process.
type statFunc func(string) (os.FileInfo, error)

// readDarwinCwd resolves pid's cwd from lsof's machine-readable field output.
// lsof emits the name as an `n` field after selecting descriptor `cwd`.
func readDarwinCwd(pid int) (cwd string, deleted bool, ok bool) {
	out, err := exec.Command(
		"lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn",
	).Output()
	if err != nil {
		return "", false, false
	}
	return parseDarwinCwd(out, os.Stat)
}

// parseDarwinCwd extracts the working directory from `lsof -Fn` output and
// decides whether that directory still exists.
//
// Unlike Linux's /proc/<pid>/cwd symlink, macOS lsof does NOT append a
// " (deleted)" marker when the directory a process is sitting in has been
// unlinked — it prints the bare, now-dangling path. The suffix is still
// honored (harmless, and it keeps this parser shaped like readProcCwd), but
// when it is absent the path is stat'ed and ENOENT is the deletion signal.
// Without that probe the deleted-cwd arm of selectOrphanTestServerPIDs could
// never fire on darwin, so a `go test -timeout` panic — which skips every
// t.Cleanup and TestMain defer — left its dolt sql-server running forever
// even though the temp tree it served was long gone (wy-j2zc8q).
//
// Any other stat error (a permission wall, an unreadable parent) is treated
// as "not deleted": this sweep only ever escalates on positive proof, never
// on a failed read.
func parseDarwinCwd(lsofOutput []byte, stat statFunc) (cwd string, deleted bool, ok bool) {
	for _, line := range strings.Split(string(lsofOutput), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		cwd = strings.TrimPrefix(line, "n")
		const deletedSuffix = " (deleted)"
		if strings.HasSuffix(cwd, deletedSuffix) {
			return strings.TrimSuffix(cwd, deletedSuffix), true, true
		}
		if cwd == "" {
			continue
		}
		if _, err := stat(cwd); err != nil && os.IsNotExist(err) {
			return cwd, true, true
		}
		return cwd, false, true
	}
	return "", false, false
}
