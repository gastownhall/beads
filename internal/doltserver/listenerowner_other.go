//go:build !linux && !darwin

package doltserver

var listenerOwnershipChecked = false

// classifyListener cannot tell which process holds a listener on this
// platform without shelling out on every readiness poll, so it reports
// OwnershipUnknown and Start falls back to the greeting plus the child's own
// output and exit status.
func classifyListener(pid, port int) Ownership {
	return OwnershipUnknown
}
