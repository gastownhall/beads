//go:build darwin

package doltserver

const portHolderSource = "lsof"

// resolvePortHolderInDir delegates to this package's existing lsof lookups.
// Darwin has no procfs, so there is no way to answer without running a helper,
// and binding is limited to what lsof reports as the working directory: boundBy
// is always "cwd" here, never "fd-lock".
//
// findPIDOnPort collapses "nothing listening" and "lsof failed" into 0, which
// this cannot separate. Reporting PortHolderUndetermined for both is the safe
// reading: a gate that needs proof the port is free records unavailable rather
// than passing on a lookup that may simply not have run.
func resolvePortHolderInDir(port int, dir string) (int, string, PortHolderOutcome) {
	pid := findPIDOnPort(port)
	if pid <= 0 {
		return 0, "", PortHolderUndetermined
	}
	if dir == "" {
		return pid, "", PortHolderHeld
	}
	if !isProcessInDir(pid, dir) {
		return 0, "", PortHolderNoHolder
	}
	return pid, "cwd", PortHolderHeld
}
