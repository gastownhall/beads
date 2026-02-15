//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// bootstrapEmbeddedDolt checks if a JSONL-to-Dolt bootstrap is needed and runs it if so.
func bootstrapEmbeddedDolt(ctx context.Context, path string, cfg *dolt.Config) error {
	// Path is the dolt subdirectory, parent is .beads directory
	beadsDir := filepath.Dir(path)

	// In dolt-native mode, JSONL is export-only backup — never auto-import.
	// If the dolt DB doesn't exist in this mode, that's an error, not a bootstrap opportunity.
	// This prevents split-brain: without this guard, a wrong path (from B1) would silently
	// create a new DB from stale JSONL, diverging from the real dolt-native data.
	if config.GetSyncMode() == config.SyncModeDoltNative {
		if !hasDoltSubdir(path) {
			return fmt.Errorf("dolt database not found at %s (JSONL auto-import is disabled in dolt-native sync mode; run 'bd init --backend=dolt' to create a new database)", path)
		}
		return nil // Dolt exists, no bootstrap needed
	}

	bootstrapped, result, err := dolt.Bootstrap(ctx, dolt.BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    path,
		LockTimeout: cfg.OpenTimeout,
		Database:    cfg.Database,
	})
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	if bootstrapped && result != nil {
		fmt.Fprintf(os.Stderr, "Bootstrapping Dolt from JSONL...\n")
		if len(result.ParseErrors) > 0 {
			fmt.Fprintf(os.Stderr, "  Skipped %d malformed lines (see above for details)\n", len(result.ParseErrors))
		}
		fmt.Fprintf(os.Stderr, "  Imported %d issues", result.IssuesImported)
		if result.IssuesSkipped > 0 {
			fmt.Fprintf(os.Stderr, ", skipped %d duplicates", result.IssuesSkipped)
		}
		fmt.Fprintf(os.Stderr, "\n  Dolt database ready\n")
	}
	return nil
}

// hasDoltSubdir checks if the given path contains any subdirectory with a .dolt directory inside.
func hasDoltSubdir(basePath string) bool {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		doltDir := filepath.Join(basePath, entry.Name(), ".dolt")
		if info, err := os.Stat(doltDir); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
