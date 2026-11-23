//go:build unix

package config

import (
	"os"
	"syscall"
)

// openLegacyDarwinUserConfig opens the legacy macOS user config read-only and
// non-blocking, so a FIFO swapped in after the caller's stat opens at once
// instead of waiting for a writer; the caller's fstat re-check then refuses
// it. O_NONBLOCK does not change how a regular file reads.
func openLegacyDarwinUserConfig(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // path is the fixed legacy darwin user config path
}
