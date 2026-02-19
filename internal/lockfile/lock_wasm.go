//go:build js && wasm

package lockfile

import (
	"errors"
	"os"
)

var errProcessLocked = errors.New("lock already held by another process")

func flockExclusive(f *os.File) error {
	// WASM doesn't support file locking
	// In a WASM environment, we're typically single-process anyway
	return nil // No-op in WASM
}

// FlockExclusiveNonBlocking attempts to acquire an exclusive lock without blocking.
// In WASM, this is a no-op since we're single-process.
func FlockExclusiveNonBlocking(f *os.File) error {
	return nil
}

// FlockExclusiveBlocking acquires an exclusive blocking lock on the file.
// In WASM, this is a no-op since we're single-process.
func FlockExclusiveBlocking(f *os.File) error {
	return nil
}

// FlockUnlock releases a lock on the file.
// In WASM, this is a no-op.
func FlockUnlock(f *os.File) error {
	return nil
}
