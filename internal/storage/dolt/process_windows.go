//go:build windows
// +build windows

package dolt

import "os"

func processMayBeAlive(p *os.Process) bool {
	// Windows doesn't support Unix-style signal(0) checks. Treat as "unknown/alive"
	// and let connection attempts / wait timeouts determine readiness.
	_ = p
	return true
}

func terminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	// Best-effort: Windows doesn't have SIGTERM semantics; kill the process.
	return p.Kill()
}
