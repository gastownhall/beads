//go:build linux

package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SweepOrphanedTestServers reaps `dolt sql-server` processes that are
// provably leaked test debris: their working directory has been deleted, or
// sits under one of suiteTempRoots. It is meant to be called once, from a
// test suite's TestMain, after m.Run() returns — a backstop for servers
// that survived an interrupted (e.g. SIGKILLed) test run despite the
// test-mode Pdeathsig in procattr_linux.go.
//
// suiteTempRoots MUST be directories owned by (anchored under) the calling
// suite alone — e.g. a package's own testTempRoot — never a shared/global
// temp dir such as os.TempDir(). scripts/test.sh runs packages in parallel
// (-p N), so a global root would make every *other* suite's still-running
// server (whose data dir also happens to live under os.TempDir(), which is
// true of essentially all of them) look like debris and get SIGTERM'd
// mid-run. That is why this function never defaults to os.TempDir() itself:
// a live server is only ever reaped when its cwd is nested under a root the
// caller vouches for as its own.
//
// A server whose working directory has been deleted (cwdDeleted, see
// readProcCwd) is reaped unconditionally regardless of suiteTempRoots —
// that is the unambiguous leak signature (a t.TempDir() cleanup ran out
// from under a still-live detached server) and cannot occur for any
// server, from any suite, that is still legitimately in use.
//
// Safety is the whole point: this must never touch a developer's real
// shared server. It only reads /proc (no killing) to build the candidate
// list, and selectOrphanTestServerPIDs only matches processes whose data
// directory is gone or explicitly caller-scoped — a production server's
// data directory is neither. Errors reading /proc for any single PID just
// drop that PID from consideration; this function is best-effort and never
// returns an error itself.
//
// Returns the PIDs it sent a kill signal to.
func SweepOrphanedTestServers(suiteTempRoots ...string) []int {
	candidates := gatherDoltServerCandidates()
	pids := selectOrphanTestServerPIDs(candidates, suiteTempRoots)

	self := os.Getpid()
	var killed []int
	for _, pid := range pids {
		if pid == self {
			continue
		}
		// Revalidate identity right before signaling: candidate selection
		// above already did its own /proc read, and in a PID-reuse window
		// the kernel could have recycled this PID to an unrelated process
		// in between. isDoltServerProcess re-reads /proc/<pid>/cmdline so
		// we only ever signal something that still looks like the
		// dolt sql-server we selected.
		if !isDoltServerProcess(pid) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err == nil {
			killed = append(killed, pid)
		}
	}

	if len(killed) == 0 {
		return killed
	}

	fmt.Fprintf(os.Stderr, "Info: swept %d orphaned test dolt sql-server process(es): %v\n", len(killed), killed)

	// Give SIGTERM a moment, then force anything still alive. This runs at
	// suite exit, so a short bounded wait here is acceptable.
	time.Sleep(300 * time.Millisecond)
	for _, pid := range killed {
		// Revalidate again before escalating to SIGKILL: the original
		// server may have exited cleanly during the grace period, and in
		// a PID-reuse window (the kernel cycling the whole PID space
		// within 300ms) this PID could now belong to an unrelated
		// process. Recheck it still looks like a dolt sql-server before
		// force-killing it.
		if !isDoltServerProcess(pid) {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	return killed
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
		cmdline := strings.ReplaceAll(strings.Trim(string(cmdlineRaw), "\x00"), "\x00", " ")
		if !isDoltServerCmdline(cmdline) {
			continue
		}

		cwd, deleted := readProcCwd(pid)
		candidates = append(candidates, serverCandidate{
			pid:        pid,
			cmdline:    cmdline,
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
