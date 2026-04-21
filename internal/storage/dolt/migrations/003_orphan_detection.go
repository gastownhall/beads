package migrations

import "database/sql"

// DetectOrphanedChildren was a diagnostic migration that ran a full table scan
// with a LEFT JOIN on every store open to find child issues whose parent no
// longer exists. This caused significant CPU load and log noise in workspaces
// with frequent bd invocations (GH#3344: 107 orphan warnings on every command,
// full scan at ~90 connections/sec).
//
// Orphan detection now lives exclusively in `bd doctor --deep` and
// `bd doctor --check=conventions`, where it belongs as an on-demand diagnostic.
// This migration is retained as a no-op to preserve the compat migration
// registry ordering.
func DetectOrphanedChildren(_ *sql.DB) error {
	return nil
}
