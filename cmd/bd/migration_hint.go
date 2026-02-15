package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
)

// migrationHintCooldown is the minimum duration between migration hints (24 hours).
const migrationHintCooldown = 24 * time.Hour

// maybeShowMigrationHint prints a one-time stderr hint when the rig is on SQLite.
// As of v0.50, Dolt is the default backend for new projects, so all SQLite users
// should be nudged to migrate. Rate-limited to once per 24 hours via a timestamp file.
// Non-blocking: returns immediately, commands continue normally with SQLite.
func maybeShowMigrationHint(beadsDir string) {
	// Don't show hints in JSON or quiet mode
	if jsonOutput || quietFlag {
		return
	}

	// Check if backend is already Dolt
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		return
	}

	// Rate-limit: check timestamp file
	tsFile := filepath.Join(beadsDir, ".migration-hint-ts")
	//nolint:gosec // G304: tsFile is constructed from beadsDir, not user input
	if data, err := os.ReadFile(tsFile); err == nil {
		if ts, err := strconv.ParseInt(string(data), 10, 64); err == nil {
			lastShown := time.Unix(ts, 0)
			if time.Since(lastShown) < migrationHintCooldown {
				return
			}
		}
	}

	// Show the hint
	fmt.Fprintf(os.Stderr, "Note: bd v0.50+ defaults to Dolt. Run 'bd migrate --to-dolt' to upgrade your database.\n")
	fmt.Fprintf(os.Stderr, "      Your SQLite data will be fully preserved. Run 'bd doctor' for details.\n")

	// Write current timestamp (best-effort)
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(tsFile, []byte(tsStr), 0600); err != nil {
		debug.Logf("warning: failed to write migration hint timestamp: %v", err)
	}
}
