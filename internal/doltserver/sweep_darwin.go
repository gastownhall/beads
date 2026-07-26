//go:build darwin

package doltserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	pids := selectOrphanTestServerPIDs(candidates, canonicalDarwinRoots(suiteTempRoots))

	self := os.Getpid()
	var killed []int
	for _, pid := range pids {
		if pid == self {
			continue
		}
		// Re-read the process command immediately before signaling so a PID
		// recycled since candidate collection cannot target an unrelated
		// process.
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

	time.Sleep(300 * time.Millisecond)
	for _, pid := range killed {
		// Revalidate again before escalation for the same PID-reuse reason.
		if !isDoltServerProcess(pid) {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	return killed
}

// canonicalDarwinRoots makes caller roots comparable with lsof output. macOS
// commonly reports a cwd below /private/var while os.MkdirTemp returned the
// equivalent /var path.
func canonicalDarwinRoots(roots []string) []string {
	canonical := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			canonical = append(canonical, root)
			continue
		}
		canonical = append(canonical, resolved)
	}
	return canonical
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

// readDarwinCwd resolves pid's cwd from lsof's machine-readable field output.
// lsof emits the name as an `n` field after selecting descriptor `cwd`.
func readDarwinCwd(pid int) (cwd string, deleted bool, ok bool) {
	out, err := exec.Command(
		"lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn",
	).Output()
	if err != nil {
		return "", false, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		cwd = strings.TrimPrefix(line, "n")
		const deletedSuffix = " (deleted)"
		if strings.HasSuffix(cwd, deletedSuffix) {
			return strings.TrimSuffix(cwd, deletedSuffix), true, true
		}
		if cwd != "" {
			return cwd, false, true
		}
	}
	return "", false, false
}
