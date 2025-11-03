//go:build js && wasm

package daemon

import "fmt"

// WASM doesn't support process management, so these are stubs
// Daemon mode is not supported in WASM environments

func killProcess(pid int) error {
	return fmt.Errorf("daemon operations not supported in WASM")
}

func forceKillProcess(pid int) error {
	return fmt.Errorf("daemon operations not supported in WASM")
}

func isProcessAlive(pid int) bool {
	return false
}
