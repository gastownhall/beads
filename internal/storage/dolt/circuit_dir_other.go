//go:build !unix

package dolt

import "os"

// circuitDirName keeps the unsuffixed name on platforms whose temp root is
// already per-user (%TEMP% on Windows resolves inside the user profile), so
// the shared directory cannot collide across local users the way a shared
// /tmp does on Unix hosts (GH#4223).
func circuitDirName() string {
	return "beads-circuit"
}

// validateCircuitDirOwnership applies no Unix ownership requirement: there
// is no cross-user /tmp to poison on platforms with a per-user temp root.
func validateCircuitDirOwnership(string, os.FileInfo) error {
	return nil
}
