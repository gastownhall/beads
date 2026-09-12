//go:build darwin || linux

package doltserver

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// reapServerPIDs SIGTERMs each selected PID, then SIGKILLs whatever is still
// alive a moment later. isServer re-reads the process's identity and reports
// whether it still looks like a dolt sql-server; it is consulted immediately
// before EVERY signal, because the kernel could have recycled the PID onto an
// unrelated process since the candidate list was built (and again during the
// grace period below). The caller passes its platform's probe — /proc on
// linux, ps on darwin.
//
// This lives in a darwin||linux file rather than the portable sweep.go
// because the signaling itself is POSIX; the selection logic that decides
// WHICH pids get here is in sweep.go and is platform-independent.
//
// Returns the PIDs it sent a kill signal to.
func reapServerPIDs(pids []int, isServer func(int) bool) []int {
	self := os.Getpid()
	var killed []int
	for _, pid := range pids {
		if pid == self {
			continue
		}
		if !isServer(pid) {
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

	// Give SIGTERM a moment, then force anything still alive. This runs at a
	// suite boundary, so a short bounded wait here is acceptable.
	time.Sleep(300 * time.Millisecond)
	for _, pid := range killed {
		if !isServer(pid) {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	return killed
}
