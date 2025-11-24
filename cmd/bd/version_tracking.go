package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
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
	fmt.Println()
}
