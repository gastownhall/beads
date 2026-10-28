//go:build cgo

package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// countEmbeddedInitIssues counts the issues in the named embedded database.
// The preview open never creates the database and tolerates a schema this
// binary would migrate, so counting the database a repoint leaves behind
// neither writes to it nor fails on its age.
func countEmbeddedInitIssues(ctx context.Context, beadsDir, database string) (int, error) {
	store, err := embeddeddolt.OpenForPreviewCommand(ctx, beadsDir, database, "main")
	if err != nil {
		return 0, err
	}
	defer func() { _ = store.Close() }()
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, err
	}
	if stats == nil {
		return 0, nil
	}
	return stats.TotalIssues, nil
}
