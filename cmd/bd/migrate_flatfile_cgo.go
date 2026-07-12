//go:build cgo

package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// openEmbeddedDoltForReverseMigration opens the embedded Dolt destination for
// bd migrate flatfile --reverse. Embedded Dolt is only available in CGO
// builds; the !cgo twin returns a descriptive error instead.
func openEmbeddedDoltForReverseMigration(ctx context.Context, beadsDir, dbName string) (storage.DoltStorage, error) {
	return embeddeddolt.Open(ctx, beadsDir, dbName, "main")
}
