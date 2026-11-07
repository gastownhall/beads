//go:build !windows

package beads

// hostWindowsFacts is never consulted off Windows (isPathInSafeBoundary only
// dispatches to the Windows policy there); tests inject facts directly.
func hostWindowsFacts() windowsFacts {
	return windowsFacts{}
}
