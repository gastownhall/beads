//go:build !cgo

package embeddeddolt

import "errors"

// Unlocker is the interface for releasing an acquired lock.
type Unlocker interface {
	Unlock()
}

// Lock is a stub for builds without CGO.
type Lock struct{}

// TryLock returns an error when CGO is not enabled.
func TryLock(_ string) (*Lock, error) {
	return nil, errors.New("embeddeddolt: requires CGO (build with CGO_ENABLED=1)")
}

// Unlock is a no-op stub.
func (l *Lock) Unlock() {}

// NoopLock is a lock that does nothing.
type NoopLock struct{}

// Unlock is a no-op.
func (NoopLock) Unlock() {}
