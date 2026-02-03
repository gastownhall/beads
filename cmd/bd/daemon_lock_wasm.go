//go:build js && wasm

package main

import (
	"fmt"
	"os"
)

func flockExclusive(f *os.File) error {
	// WASM doesn't support file locking
	// In a WASM environment, we're typically single-process anyway
	return fmt.Errorf("file locking not supported in WASM")
}

func flockExclusiveBlocking(f *os.File) error {
	return fmt.Errorf("file locking not supported in WASM")
}
