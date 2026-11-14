//go:build !windows

package workspacegate

import "path/filepath"

// canonicalParent resolves an existing gate parent directory to one
// spelling shared by every path that reaches it. Outside Windows that is
// exactly filepath.EvalSymlinks.
func canonicalParent(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}
