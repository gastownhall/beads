//go:build js && wasm

package lockfile

import (
	"errors"
	"fmt"
	"os"
)

var errDaemonLocked = errors.New("daemon lock already held by another process")

func flockExclusive(f *os.File) error {
	// WASM doesn't support file locking
	// In a WASM environment, we're typically single-process anyway
	return fmt.Errorf("file locking not supported in WASM")
}
