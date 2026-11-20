//go:build !unix

package beads

// dirOwnedBy has no uid ownership model to check on a non-Unix platform, so
// it fails closed and the service-home exception never applies there.
func dirOwnedBy(_ string, _ int) bool {
	return false
}
