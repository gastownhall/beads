//go:build darwin

package doltserver

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// sweepServersUnderRoots reaps only the dolt sql-servers whose working
// directory sits under one of suiteTempRoots, deleted or not. It never
// reaches outside the trees the caller names — see selectServersUnderRoots.
// On Darwin, process candidates come from ps and their working directories
// from lsof because /proc is unavailable.
//
// Returns the servers (pid + cwd) it sent a kill signal to. Process-listing
// errors and candidates whose cwd cannot be resolved are ignored.
func sweepServersUnderRoots(suiteTempRoots ...string) []SweptServer {
	candidates := gatherDoltServerCandidates()
	selected := selectServersUnderRoots(candidates, canonicalRoots(suiteTempRoots))
	return reapServers(selected, isDoltServerProcess)
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
// The suite sweeps select by cwd alone, so the probe only feeds
// serverCandidate.cwdDeleted; it is kept so both platforms report the same
// candidate shape.
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
