//go:build !linux && !darwin

package doltserver

// listenerOwnership cannot tell which process holds a listener on this
// platform without shelling out on every readiness poll, so it reports
// known == false and Start falls back to the greeting plus the child's own
// output and exit status.
func listenerOwnership(pid, port int) (owned, known bool) {
	return false, false
}
