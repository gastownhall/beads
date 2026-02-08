package main

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/autoimport"
)

// requireFreshDB checks database freshness and exits on failure.
// Skips the check when running in daemon mode (daemon auto-imports on staleness).
// This is the standard wrapper for read commands; use it instead of calling
// ensureDatabaseFresh directly to avoid boilerplate.
func requireFreshDB(ctx context.Context) {
	if daemonClient == nil {
		if err := ensureDatabaseFresh(ctx); err != nil {
			FatalErrorRespectJSON("%v", err)
		}
	}
}

// ensureDatabaseFresh checks if the database is in sync with JSONL before read operations.
// If JSONL is newer than database, refuses to operate with an error message.
// This prevents users from making decisions based on stale/incomplete data.
//
// NOTE: Prefer requireFreshDB() which wraps this with the daemon check and error handling.
//
// Implements bd-2q6d: All read operations should validate database freshness.
// Implements bd-c4rq: Daemon check moved to call sites to avoid function call overhead.
func ensureDatabaseFresh(ctx context.Context) error {
	if allowStale {
		fmt.Fprintf(os.Stderr, "⚠️  Staleness check skipped (--allow-stale), data may be out of sync\n")
		return nil
	}

	// Skip check if no storage available (shouldn't happen in practice)
	if store == nil {
		return nil
	}

	// In dolt-native mode, Dolt is the source of truth — JSONL is export-only backup.
	// Skip the staleness check entirely since JSONL is never imported in this mode.
	if !ShouldImportJSONL(ctx, store) {
		return nil
	}

	// Check if database is stale
	isStale, err := autoimport.CheckStaleness(ctx, store, dbPath)
	if err != nil {
		// If we can't determine staleness, allow operation to proceed
		// (better to show potentially stale data than block user)
		fmt.Fprintf(os.Stderr, "Warning: Could not verify database freshness: %v\n", err)
		fmt.Fprintf(os.Stderr, "Proceeding anyway. Data may be stale.\n\n")
		return nil
	}

	if !isStale {
		// Database is fresh, proceed
		return nil
	}

	// Database is stale - auto-import to refresh (bd-9dao fix)
	// For read-only commands running in --no-daemon mode, auto-import instead of
	// returning an error. This allows commands like `bd show` to work after git pull.
	// Skip auto-import if store is read-only - it can't write anyway (GH#1089)
	if !noAutoImport && !storeIsReadOnly {
		autoImportIfNewer()
		return nil
	}

	// Auto-import is disabled, refuse to operate
	return fmt.Errorf(
		"Database out of sync with JSONL. Run 'bd sync --import-only' to fix.\n\n" +
			"The JSONL file has been updated (e.g., after 'git pull') but the database\n" +
			"hasn't been imported yet. This would cause you to see stale/incomplete data.\n\n" +
			"To fix:\n" +
			"  bd sync --import-only            # Import JSONL updates to database\n" +
			"  bd import -i .beads/issues.jsonl  # Alternative: specify file explicitly\n\n" +
			"If in a sandboxed environment (e.g., Codex) where daemon can't be stopped:\n" +
			"  bd --sandbox ready               # Use direct mode (no daemon)\n" +
			"  bd ready --allow-stale           # Skip staleness check (use with caution)\n\n" +
			"Or use daemon mode (auto-imports on every operation):\n" +
			"  bd daemon start\n" +
			"  bd <command>     # Will auto-import before executing",
	)
}
