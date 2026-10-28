//go:build !unix

package beads

// dirOwnedBy fails closed on non-unix platforms: the own-home carve-out in
// isPathInSafeBoundary rests on unix account-database semantics (uid
// ownership), and os.Getuid is -1 there anyway, so the carve-out never admits
// anything. home_owner_unix.go holds the real implementation.
func dirOwnedBy(string, int) bool {
	return false
}
