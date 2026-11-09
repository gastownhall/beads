//go:build linux

package doltserver

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sweepServersUnderRoots reaps only the dolt sql-servers whose working
// directory sits under one of suiteTempRoots, deleted or not. It never
// reaches outside the trees the caller names — see selectServersUnderRoots.
//
// Returns the servers (pid + cwd) it sent a kill signal to. Process-listing
// errors and candidates whose cwd cannot be resolved are ignored.
func sweepServersUnderRoots(suiteTempRoots ...string) []SweptServer {
	candidates := gatherDoltServerCandidates()
	selected := selectServersUnderRoots(candidates, canonicalRoots(suiteTempRoots))
	return reapServers(selected, isDoltServerProcess)
}

// isDoltServerProcess re-reads /proc/<pid>/cmdline and reports whether pid
// still refers to a dolt sql-server process. Used to revalidate a PID
// immediately before signaling it, guarding against the kernel having
// recycled the PID to an unrelated process since it was first observed.
func isDoltServerProcess(pid int) bool {
	cmdlineRaw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(cmdlineRaw) == 0 {
		return false
	}
	cmdline := strings.ReplaceAll(strings.Trim(string(cmdlineRaw), "\x00"), "\x00", " ")
	return isDoltServerCmdline(cmdline)
}

// gatherDoltServerCandidates scans /proc for processes whose cmdline
// mentions a dolt sql-server, resolving each one's working directory (and
// whether that directory has been deleted) via /proc/<pid>/cwd.
func gatherDoltServerCandidates() []serverCandidate {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	var candidates []serverCandidate
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue // not a PID directory
		}

		cmdlineRaw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(cmdlineRaw) == 0 {
			continue // process gone, or unreadable (permissions, kernel thread)
		}
		argv := strings.Fields(strings.ReplaceAll(string(cmdlineRaw), "\x00", " "))
		if !isDoltServerArgv(argv) {
			continue
		}

		cwd, deleted := readProcCwd(pid)
		candidates = append(candidates, serverCandidate{
			pid:        pid,
			argv:       argv,
			cwd:        cwd,
			cwdDeleted: deleted,
		})
	}
	return candidates
}

// readProcCwd resolves a process's working directory via
// /proc/<pid>/cwd. Linux appends " (deleted)" to the symlink target when
// the directory it once pointed at has since been removed — exactly the
// case of a t.TempDir() cleanup racing ahead of a leaked detached server.
func readProcCwd(pid int) (cwd string, deleted bool) {
	link, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err != nil {
		return "", false
	}
	const deletedSuffix = " (deleted)"
	if strings.HasSuffix(link, deletedSuffix) {
		return strings.TrimSuffix(link, deletedSuffix), true
	}
	return link, false
}
