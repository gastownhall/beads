//go:build !unix

package config

import "os"

// openLegacyDarwinUserConfig opens the legacy macOS user config read-only.
// The legacy file only exists on macOS; off Unix there is no FIFO swap to
// guard against.
func openLegacyDarwinUserConfig(path string) (*os.File, error) {
	return os.Open(path) //nolint:gosec // path is the fixed legacy darwin user config path
}
