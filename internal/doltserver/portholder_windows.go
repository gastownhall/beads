//go:build windows

package doltserver

const portHolderSource = "netstat"

// resolvePortHolderInDir finds the port holder through netstat (findPIDOnPort
// here parses `netstat -aon`, not lsof) but can never bind it to a directory:
// isProcessInDir is unconditionally false on Windows because the platform does
// not expose a process's working directory through standard APIs.
//
// So a caller asking "is this endpoint free?" (dir empty) gets an answer, while
// a caller asking "is that server this workspace's?" always gets
// PortHolderUndetermined — which the handoff journals as an unresolved
// instance, not as a disproof. As on darwin, findPIDOnPort's 0 cannot
// distinguish an empty port from a failed netstat, so it is undetermined.
func resolvePortHolderInDir(port int, dir string) (int, string, PortHolderOutcome) {
	pid := findPIDOnPort(port)
	if pid <= 0 {
		return 0, "", PortHolderUndetermined
	}
	if dir == "" {
		return pid, "", PortHolderHeld
	}
	return 0, "", PortHolderUndetermined
}
