//go:build cgo && !unix && !windows

package embeddeddolt

import "errors"

func filesystemHasAvailableSpace(string) (bool, error) {
	return false, errors.ErrUnsupported
}
