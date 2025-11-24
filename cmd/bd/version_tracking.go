package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// trackBdVersion checks if bd version has changed since last run and updates metadata.json.
// This function is best-effort - failures are silent to avoid disrupting commands.
// Sets global variables versionUpgradeDetected and previousVersion if upgrade detected.
//
// bd-loka: Built-in version tracking for upgrade awareness
func trackBdVersion() {
	// Find the beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		// No .beads directory found - this is fine (e.g., bd init, bd version, etc.)
		return
	}

	// Load current config
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		// Silent failure - config might not exist yet
		return
	}
	if cfg == nil {
		// No config file yet - create one with current version
		cfg = configfile.DefaultConfig()
		cfg.LastBdVersion = Version

		// bd-afd: Auto-detect actual JSONL file instead of using hardcoded default
		// This prevents mismatches when metadata.json gets deleted (git clean, merge conflict, etc.)
		if actualJSONL := findActualJSONLFile(beadsDir); actualJSONL != "" {
			cfg.JSONLExport = actualJSONL
		}

		_ = cfg.Save(beadsDir) // Best effort
		return
	}

	// Check if version changed
	if cfg.LastBdVersion != "" && cfg.LastBdVersion != Version {
		// Version upgrade detected!
		versionUpgradeDetected = true
		previousVersion = cfg.LastBdVersion
	}

	// Update metadata.json with current version (best effort)
	// Only write if version actually changed to minimize I/O
	// Also update on first run (when LastBdVersion is empty) to initialize tracking
	if cfg.LastBdVersion != Version {
		cfg.LastBdVersion = Version
		_ = cfg.Save(beadsDir) // Silent failure is fine
	}
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

	// Check if BD_GUIDE.md exists and needs updating
	checkAndSuggestBDGuideUpdate()

	fmt.Println()
}

// checkAndSuggestBDGuideUpdate checks if .beads/BD_GUIDE.md exists and suggests regeneration if outdated.
// bd-woro: Auto-update BD_GUIDE.md on version changes
func checkAndSuggestBDGuideUpdate() {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	guidePath := beadsDir + "/BD_GUIDE.md"

	// Check if BD_GUIDE.md exists
	if _, err := os.Stat(guidePath); os.IsNotExist(err) {
		// File doesn't exist - no suggestion needed
		return
	}

	// Read first few lines to check version stamp
	// #nosec G304 - guidePath is constructed from beadsDir + constant string
	content, err := os.ReadFile(guidePath)
	if err != nil {
		return // Silent failure
	}

	// Look for version in the first 200 bytes (should be in the header)
	header := string(content)
	if len(header) > 200 {
		header = header[:200]
	}

	// Check if the file has the old version stamp
	oldVersionStamp := fmt.Sprintf("bd v%s", previousVersion)
	currentVersionStamp := fmt.Sprintf("bd v%s", Version)

	if containsSubstring(header, oldVersionStamp) && !containsSubstring(header, currentVersionStamp) {
		// BD_GUIDE.md is outdated
		fmt.Printf("📄 BD_GUIDE.md is outdated (v%s → v%s)\n", previousVersion, Version)
		fmt.Printf("💡 Run 'bd onboard --output .beads/BD_GUIDE.md' to regenerate\n")
	}
}

// containsSubstring checks if haystack contains needle (case-sensitive)
func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) && findSubstring(haystack, needle) >= 0
}

// findSubstring returns the index of needle in haystack, or -1 if not found
func findSubstring(haystack, needle string) int {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// findActualJSONLFile scans .beads/ for the actual JSONL file in use.
// Prefers beads.jsonl over issues.jsonl, skips backups and merge artifacts.
// Returns empty string if no JSONL file is found.
//
// bd-afd: Auto-detect JSONL file to prevent metadata.json mismatches
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

	// Prefer beads.jsonl over issues.jsonl (canonical name)
	for _, name := range candidates {
		if name == "beads.jsonl" {
			return name
		}
	}

	// Fall back to first candidate (including issues.jsonl if present)
	return candidates[0]
}

// autoMigrateOnVersionBump automatically migrates the database when CLI version changes.
// This function is best-effort - failures are silent to avoid disrupting commands.
// Called from PersistentPreRun after daemon check but before opening DB for main operation.
//
// IMPORTANT: This must be called AFTER determining we're in direct mode (no daemon)
// and BEFORE opening the database, to avoid: 1) conflicts with daemon, 2) opening DB twice.
//
// bd-jgxi: Auto-migrate database on CLI version bump
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
