//go:build !windows

package workspacegate

import "path/filepath"

// canonicalParent resolves p, the parent of a guarded directory, to the
// one canonical spelling every alias of that directory shares. Off
// Windows this is exactly filepath.EvalSymlinks, byte for byte: nothing
// about gate identity changes on these platforms.
func canonicalParent(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}
