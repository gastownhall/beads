//go:build js && wasm

package lockfile

import "os"

// FlockSharedNonBlock is a no-op in WASM (single-process environment).
func FlockSharedNonBlock(f *os.File) error {
	return nil
}

// FlockExclusiveNonBlock is a no-op in WASM (single-process environment).
func FlockExclusiveNonBlock(f *os.File) error {
	return nil
}
