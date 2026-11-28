//go:build !unix

package beads

// dirOwnedBy cannot vet ownership on this platform, so the denied-prefix home
// carve-out in isPathInSafeBoundary fails closed here.
func dirOwnedBy(string, int) bool { return false }
