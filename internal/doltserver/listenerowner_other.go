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

// classifyListenerAt cannot tell which process holds a listener on this
// platform either, so pairing accepts a server bd launches here on the
// evidence of its startup and of the server UUID bd started it with, or of
// its startup alone when bd pinned none (pairServerIdentity). Windows looks
// its listeners up through netstat instead (listenerOwnershipAt).
func classifyListenerAt(pid int, host string, port int) Ownership {
	return OwnershipUnknown
}
