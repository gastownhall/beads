package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// checkDatabaseFreshness verifies the Dolt database is not stale relative to
// the JSONL file on disk. If .beads/issues.jsonl has a newer mtime than the
// database's last_import_time metadata, the database may contain outdated data.
//
// Returns a non-nil error when the database is stale. The caller should refuse
// to proceed (unless --allow-stale is set).
//
// Conditions that skip the check:
//   - No issues.jsonl file found (nothing to compare against)
//   - No last_import_time metadata recorded (legacy/first use — no baseline)
//   - Corrupted last_import_time metadata (warn and skip)
func checkDatabaseFreshness(ctx context.Context, store *dolt.DoltStore, beadsDir string) error {
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	info, err := os.Stat(jsonlPath)
	if err != nil {
		// No JSONL file — nothing to compare against. This is common in
		// Dolt-native setups (e.g., Gas Town) where JSONL is not used.
		debug.Logf("staleness: no issues.jsonl at %s, skipping check", jsonlPath)
		return nil
	}
	jsonlMtime := info.ModTime()

	// Get the last import time from database metadata.
	lastImportStr, err := store.GetMetadata(ctx, "last_import_time")
	if err != nil {
		debug.Logf("staleness: failed to query last_import_time: %v", err)
		return nil // Non-fatal: can't determine freshness, allow operation
	}
	if lastImportStr == "" {
		// No last_import_time recorded — this is a legacy database or first use.
		// We have no baseline to compare against, so skip.
		debug.Logf("staleness: no last_import_time recorded, skipping check")
		return nil
	}

	// Parse the stored timestamp. Try RFC3339Nano first (current), then RFC3339 (legacy).
	var lastImportTime time.Time
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, parseErr := time.Parse(layout, lastImportStr); parseErr == nil {
			lastImportTime = t
			break
		}
	}
	if lastImportTime.IsZero() {
		// Corrupted metadata — warn but don't block.
		fmt.Fprintf(os.Stderr, "Warning: corrupted last_import_time metadata: %q\n", lastImportStr)
		return nil
	}

	// Compare: if JSONL is newer than the last import, database is stale.
	// Use a 1-second tolerance to account for filesystem timestamp granularity.
	if jsonlMtime.After(lastImportTime.Add(1 * time.Second)) {
		return fmt.Errorf("database out of sync: issues.jsonl is newer than last import (%s > %s)\n"+
			"Run 'bd init --from-jsonl' or 'bd backup restore' to re-sync\n"+
			"Or use --allow-stale to skip this check",
			jsonlMtime.Format(time.RFC3339), lastImportTime.Format(time.RFC3339))
	}

	debug.Logf("staleness: database is fresh (jsonl=%s, import=%s)",
		jsonlMtime.Format(time.RFC3339), lastImportTime.Format(time.RFC3339))
	return nil
}
