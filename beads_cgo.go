//go:build cgo

package beads

import (
	"context"
)

// OpenBestAvailable opens the backend selected by the .beads directory's
// metadata. It supports the built-in Dolt, PostgreSQL, MySQL, and SQLite
// backends as well as locally trusted external backend plugins.
//
// The returned Storage must be closed when no longer needed.
//
// beadsDir is the path to the .beads directory.
func OpenBestAvailable(ctx context.Context, beadsDir string) (Storage, error) {
	store, _, err := OpenConfigured(ctx, beadsDir, OpenConfiguredOptions{})
	return store, err
}
