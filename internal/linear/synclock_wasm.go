//go:build js && wasm

package linear

func isProcessAlive(_ int) bool {
	return false
}
