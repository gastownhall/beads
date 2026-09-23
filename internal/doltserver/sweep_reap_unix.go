//go:build darwin || linux

package doltserver

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// reapServers SIGTERMs each selected server, then SIGKILLs whatever is still
// alive a moment later. isServer re-reads the process's identity and reports
// whether it still looks like a dolt sql-server; it is consulted immediately
// before EVERY signal, because the kernel could have recycled the PID onto an
// unrelated process since the candidate list was built (and again during the
// grace period below). The caller passes its platform's probe — /proc on
// linux, ps on darwin.
//
// This lives in a darwin||linux file rather than the portable sweep.go
// because the signaling itself is POSIX; the selection logic that decides
// WHICH servers get here is in sweep.go and is platform-independent.
//
// The Info line prints each entry as "<pid> cwd=<dir>" (SweptServer.String).
// The directory, not the PID, is what a reader can act on: it is the temp
// tree the leaked server was serving, which Go names after the test that
// created it, so the report identifies the leaking fixture instead of a
// number that no longer refers to anything (wy-j2zc8q).
//
// Returns the servers it sent a kill signal to.
func reapServers(servers []SweptServer, isServer func(int) bool) []SweptServer {
	self := os.Getpid()
	var killed []SweptServer
	for _, server := range servers {
		if server.PID == self {
			continue
		}
		if !isServer(server.PID) {
			continue
		}
		if err := syscall.Kill(server.PID, syscall.SIGTERM); err == nil {
			killed = append(killed, server)
		}
	}

	if len(killed) == 0 {
		return killed
	}

	fmt.Fprintf(os.Stderr, "Info: swept %d orphaned test dolt sql-server process(es): %v\n", len(killed), killed)

	// Give SIGTERM a moment, then force anything still alive. This runs at a
	// suite boundary, so a short bounded wait here is acceptable.
	time.Sleep(300 * time.Millisecond)
	for _, server := range killed {
		if !isServer(server.PID) {
			continue
		}
		_ = syscall.Kill(server.PID, syscall.SIGKILL)
	}

	return killed
}
