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
// recycled the PID to an unrelated process since it was first observed. It
// judges the same exact argv (procArgv) that gathering did.
func isDoltServerProcess(pid int) bool {
	cmdlineRaw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	return isDoltServerArgv(procArgv(cmdlineRaw))
}

// gatherDoltServerCandidates scans /proc for processes whose argv names a
// dolt sql-server (isDoltServerArgv over procArgv), resolving each one's
// working directory (and whether that directory has been deleted) via
// /proc/<pid>/cwd.
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
		argv := procArgv(cmdlineRaw)
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

// procArgv splits the raw bytes of /proc/<pid>/cmdline into argv. The kernel
// stores each argument NUL-terminated, so exactly one trailing NUL is dropped
// before splitting; every other empty argument, including an empty argv[0],
// is kept in place so no later argument shifts into its slot. Empty input
// (a kernel thread, or a process gone mid-read) yields nil. A process that
// rewrote its title into one argument stays one argument.
func procArgv(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}
