//go:build !windows

package beads

// windowsIsPathInSafeBoundary is unreachable off Windows: the runtime.GOOS
// dispatch in isPathInSafeBoundary is constant-folded away by the compiler.
// The stub exists so the portable policy in winboundary.go links everywhere
// and its tests run on Linux CI.
func windowsIsPathInSafeBoundary(input, abs string) bool {
	return true
}
