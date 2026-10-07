//go:build darwin

package doltserver

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The two process reads listenerOwnership makes on darwin; tests replace them.
var (
	listeningPIDs  = lsofListeningPIDs
	processParents = psParentPIDs
)

// listenerOwnership reports whether a TCP listener on port belongs to pid or
// one of its descendants. Darwin has no /proc, so the listener's processes
// come from lsof and the process tree from ps, the same two tools this
// package already uses to find a port's holder and a server's working
// directory. Descendants cover a dolt behind a wrapper script that does not
// exec: the listener's ancestor chain then passes through the wrapper, which
// is the child bd launched.
//
// known is false when the tools cannot answer, and the caller then falls back
// to weaker readiness evidence:
//   - lsof or ps is missing or fails;
//   - no listener on port is listed at all, although one just answered: lsof
//     cannot see another user's sockets without privilege, or the connection
//     is forwarded elsewhere.
//
// A child that no longer exists is known not to own anything.
//
// Both reads run only once a greeting has arrived on the port, never on the
// bare readiness poll, and together cost on the order of 100ms.
func listenerOwnership(pid, port int) (owned, known bool) {
	if !processExists(pid) {
		return false, true
	}
	holders, err := listeningPIDs(port)
	if err != nil || len(holders) == 0 {
		return false, false
	}
	parents, err := processParents()
	if err != nil || len(parents) == 0 {
		return false, false
	}
	for _, holder := range holders {
		if descendsFrom(holder, pid, parents) {
			return true, true
		}
	}
	return false, true
}

// processExists is true while pid names a process this user may signal, or
// one that exists but belongs to someone else.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// descendsFrom reports whether p is ancestor itself or one of its
// descendants, walking p's parent chain through parents (child -> parent).
// A chain that reaches a pid the snapshot does not list, pid 0, or a cycle
// (a stale snapshot) is not a match.
func descendsFrom(p, ancestor int, parents map[int]int) bool {
	seen := map[int]bool{}
	for p > 0 && !seen[p] {
		if p == ancestor {
			return true
		}
		seen[p] = true
		next, ok := parents[p]
		if !ok {
			return false
		}
		p = next
	}
	return false
}

// lsofListeningPIDs lists the processes holding a TCP listener on port.
// -n and -P skip host and port name lookups, which otherwise dominate the
// run time; -t prints one pid per line. lsof exits 1 with no output when
// nothing matches, which is reported as no listener, not as an error, so the
// caller treats both the same way.
func lsofListeningPIDs(port int) ([]int, error) {
	out, err := exec.Command("lsof", "-nP", "-t", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN").Output() //nolint:gosec // G702: port is an internal int, not user input
	if err != nil && len(out) == 0 {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parseLsofPIDs(out), nil
}

// parseLsofPIDs reads `lsof -t` output: one pid per line.
func parseLsofPIDs(out []byte) []int {
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// psParentPIDs snapshots every process's parent from `ps -axo pid=,ppid=`.
func psParentPIDs() (map[int]int, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	return parsePSParents(out), nil
}

// parsePSParents reads `ps -axo pid=,ppid=` output into child -> parent.
func parsePSParents(out []byte) map[int]int {
	parents := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || pid <= 0 {
			continue
		}
		parents[pid] = ppid
	}
	return parents
}
