//go:build !cgo

package beads

import (
	"context"
)

// OpenBestAvailable opens the backend selected by the .beads directory's
// metadata. Pure-Go SQL backends and locally trusted external backend plugins
// remain available in non-CGO builds; only embedded Dolt requires CGO.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	store, _, err := OpenConfigured(ctx, beadsDir, OpenConfiguredOptions{})
	return store, err
}
