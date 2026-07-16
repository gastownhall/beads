//go:build !cgo

package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// openEmbeddedDoltForReverseMigration is the !cgo twin of the CGO
// implementation: reverse migration targets the embedded Dolt backend, which
// pure-Go builds do not ship.
func openEmbeddedDoltForReverseMigration(_ context.Context, _, _ string) (storage.DoltStorage, error) {
	return nil, fmt.Errorf("bd migrate flatfile --reverse requires a CGO-enabled bd build: the embedded Dolt backend is unavailable in pure-Go builds")
}
