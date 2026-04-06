package versioncontrolops

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// EnsureIgnoredTables delegates to schema.EnsureIgnoredTables.
func EnsureIgnoredTables(ctx context.Context, db DBConn) error {
	return schema.EnsureIgnoredTables(ctx, db)
}

// CreateIgnoredTables delegates to schema.CreateIgnoredTables.
func CreateIgnoredTables(ctx context.Context, db DBConn) error {
	return schema.CreateIgnoredTables(ctx, db)
}
