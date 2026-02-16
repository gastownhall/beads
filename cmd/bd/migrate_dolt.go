//go:build cgo

package main

import (
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// listMigrations returns registered Dolt migrations (CGO build).
func listMigrations() []string {
	return dolt.ListMigrations()
}
