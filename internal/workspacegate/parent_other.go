//go:build !windows

package workspacegate

import "path/filepath"

func canonicalParent(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
