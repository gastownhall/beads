package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// localVersionFile is the gitignored file that stores the last bd version used locally.
// This prevents the upgrade notification from firing repeatedly when git operations
// reset the tracked metadata.json file.
const localVersionFile = ".local_version"

// trackBdVersion checks if bd version has changed since last run and updates the local version file.
// This function is best-effort - failures are silent to avoid disrupting commands.
// Sets global variables versionUpgradeDetected and previousVersion if upgrade detected.
func trackBdVersion() {
	// Find the beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		// No .beads directory found - this is fine (e.g., bd init, bd version, etc.)
		return
	}

	// Read last version from local (gitignored) file
	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	lastVersion := readLocalVersion(localVersionPath)

	// Check if version changed
	if lastVersion != "" && lastVersion != Version {
		// Version upgrade detected!
		versionUpgradeDetected = true
		previousVersion = lastVersion
	}

	// Update local version file (best effort)
	// Only write if version actually changed to minimize I/O
	if lastVersion != Version {
		_ = writeLocalVersion(localVersionPath, Version)
	}

	// Also ensure metadata.json exists with proper defaults (for JSONL export name)
	// but don't use it for version tracking anymore
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return
	}
	if cfg == nil {
		// No config file yet - create one
		cfg = configfile.DefaultConfig()

		// Auto-detect actual JSONL file instead of using hardcoded default
		// This prevents mismatches when metadata.json gets deleted (git clean, merge conflict, etc.)
		if actualJSONL := findActualJSONLFile(beadsDir); actualJSONL != "" {
			cfg.JSONLExport = actualJSONL
		}

		_ = cfg.Save(beadsDir) // Best effort
	}
}

// readLocalVersion reads the last bd version from the local version file.
// Returns empty string if file doesn't exist or can't be read.
func readLocalVersion(path string) string {
	// #nosec G304 - path is constructed from beadsDir + constant
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeLocalVersion writes the current version to the local version file.
func writeLocalVersion(path, version string) error {
	return os.WriteFile(path, []byte(version+"\n"), 0600)
}

// getVersionsSince returns all version changes since the given version.
// If sinceVersion is empty, returns all known versions.
// Returns changes in chronological order (oldest first).
//
// Note: versionChanges array is in reverse chronological order (newest first),
// so we return elements before the found index and reverse the slice.
func getVersionsSince(sinceVersion string) []VersionChange {
	if sinceVersion == "" {
		// Return all versions (already in reverse chronological, but kept for compatibility)
		return versionChanges
	}

	// Find the index of sinceVersion
	// versionChanges is ordered newest-first: [0.23.0, 0.22.1, 0.22.0, 0.21.0]
	startIdx := -1
	for i, vc := range versionChanges {
		if vc.Version == sinceVersion {
			startIdx = i
			break
		}
	}

	if startIdx == -1 {
		// sinceVersion not found in our changelog - return all versions
		// (user might be upgrading from a very old version)
		return versionChanges
	}

	if startIdx == 0 {
		// Already on the newest version
		return []VersionChange{}
	}

	// Return versions before sinceVersion (those are newer)
	// Then reverse to get chronological order (oldest first)
	newerVersions := versionChanges[:startIdx]

	// Reverse the slice to get chronological order
	result := make([]VersionChange, len(newerVersions))
	for i := range newerVersions {
		result[i] = newerVersions[len(newerVersions)-1-i]
	}

	return result
}

// maybeShowUpgradeNotification displays a one-time upgrade notification if version changed.
// This is called by commands like 'bd ready' and 'bd list' to inform users of upgrades.
func maybeShowUpgradeNotification() {
	// Only show if upgrade detected and not yet acknowledged
	if !versionUpgradeDetected || upgradeAcknowledged {
		return
	}

	// Mark as acknowledged so we only show once per session
	upgradeAcknowledged = true

	// Display notification
	fmt.Printf("🔄 bd upgraded from v%s to v%s since last use\n", previousVersion, Version)
	fmt.Println("💡 Run 'bd upgrade review' to see what changed")
	fmt.Println("💊 Run 'bd doctor' to verify upgrade completed cleanly")

	fmt.Println()
}

// findActualJSONLFile scans .beads/ for the actual JSONL file in use.
// Prefers issues.jsonl over beads.jsonl (canonical name), skips backups and merge artifacts.
// Returns empty string if no JSONL file is found.
func findActualJSONLFile(beadsDir string) string {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return ""
	}

	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		// Must end with .jsonl
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		// Skip merge artifacts and backups
		lowerName := strings.ToLower(name)
		if strings.Contains(lowerName, "backup") ||
			strings.Contains(lowerName, ".orig") ||
			strings.Contains(lowerName, ".bak") ||
			strings.Contains(lowerName, "~") ||
			strings.HasPrefix(lowerName, "backup_") {
			continue
		}

		candidates = append(candidates, name)
	}

	if len(candidates) == 0 {
		return ""
	}

	// Prefer issues.jsonl over beads.jsonl (canonical name)
	for _, name := range candidates {
		if name == "issues.jsonl" {
			return name
		}
	}

	// Fall back to first candidate (including beads.jsonl as legacy)
	return candidates[0]
}

// autoMigrateOnVersionBump automatically migrates the database when CLI version changes.
// This function is best-effort - failures are silent to avoid disrupting commands.
// Called from PersistentPreRun after daemon check but before opening DB for main operation.
//
// IMPORTANT: This must be called AFTER determining we're in direct mode (no daemon)
// and BEFORE opening the database, to avoid: 1) conflicts with daemon, 2) opening DB twice.
func autoMigrateOnVersionBump(dbPath string) {
	// Only migrate if version upgrade was detected
	if !versionUpgradeDetected {
		return
	}

	// Validate dbPath
	if dbPath == "" {
		debug.Logf("auto-migrate: skipping migration, no database path")
		return
	}

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// No database file - nothing to migrate
		debug.Logf("auto-migrate: skipping migration, database does not exist: %s", dbPath)
		return
	}

	// Open database to check current version
	// Use rootCtx if available and not canceled, otherwise use Background
	ctx := rootCtx
	if ctx == nil || ctx.Err() != nil {
		// rootCtx is nil or canceled - use fresh background context
		ctx = context.Background()
	}

	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		// Failed to open database - skip migration
		debug.Logf("auto-migrate: failed to open database: %v", err)
		return
	}

	// Get current database version
	dbVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		// Failed to read version - skip migration
		debug.Logf("auto-migrate: failed to read database version: %v", err)
		_ = store.Close()
		return
	}

	// Check if migration is needed
	if dbVersion == Version {
		// Database is already at current version
		debug.Logf("auto-migrate: database already at version %s", Version)
		_ = store.Close()
		return
	}

	// Perform migration: update database version
	debug.Logf("auto-migrate: migrating database from %s to %s", dbVersion, Version)
	if err := store.SetMetadata(ctx, "bd_version", Version); err != nil {
		// Migration failed - log and continue
		debug.Logf("auto-migrate: failed to update database version: %v", err)
		_ = store.Close()
		return
	}

	// Close database
	if err := store.Close(); err != nil {
		debug.Logf("auto-migrate: warning: failed to close database: %v", err)
	}

	debug.Logf("auto-migrate: successfully migrated database to version %s", Version)
}
