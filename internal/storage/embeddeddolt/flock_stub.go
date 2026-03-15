//go:build !embeddeddolt

package embeddeddolt

import "errors"

// Lock is a stub for builds without the embeddeddolt tag.
type Lock struct{}

// TryLock returns an error when the embeddeddolt build tag is not set.
func TryLock(_ string) (*Lock, error) {
	return nil, errors.New("embeddeddolt: not available")
}

// Unlock is a no-op stub.
func (l *Lock) Unlock() {}
