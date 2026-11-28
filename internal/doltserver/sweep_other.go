//go:build !linux && !darwin

package doltserver

// sweepServersUnderRoots is a no-op on platforms where process command lines
// and working directories cannot be inspected by an implementation in this
// package. The stub keeps callers (test TestMains) portable.
func sweepServersUnderRoots(_ ...string) []SweptServer {
	return nil
}
