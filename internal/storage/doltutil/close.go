// Package doltutil provides shared utilities for Dolt operations.
// This package exists to avoid import cycles between dolt and dolt/migrations.
package doltutil

import (
	"fmt"
	"time"
)

// CloseTimeout is the maximum time to wait for close operations.
const CloseTimeout = 5 * time.Second

// CloseWithTimeout runs a close function with a timeout to prevent indefinite hangs.
// Returns an error if the close times out or if the close function returns an error.
func CloseWithTimeout(name string, closeFn func() error) error {
	done := make(chan error, 1)
	go func() {
		done <- closeFn()
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(CloseTimeout):
		// Close is hanging - log and continue rather than blocking forever
		return fmt.Errorf("%s close timed out after %v", name, CloseTimeout)
	}
}
