//go:build !unix

package beads

// dirOwnedBy has no answer on a platform without Unix uid ownership: there
// is no stat uid to compare against. It returns false so the /var/lib
// service-home exception (GH#6323) never admits a boundary it cannot vet,
// the same fail-closed choice internal/httpclient makes for CA-file
// permissions on non-unix builds.
func dirOwnedBy(dir string, uid int) bool {
	return false
}
