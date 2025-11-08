//go:build js && wasm

package lockfile

// isProcessRunning checks if a process with the given PID is running
// In WASM, this always returns false since we don't have process management
func isProcessRunning(pid int) bool {
	return false
}
