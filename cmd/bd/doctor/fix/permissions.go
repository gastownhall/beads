package fix

import (
	"fmt"
	"os"
	"path/filepath"
)

// Permissions fixes file permission issues in the .beads directory
func Permissions(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")

	// Check if .beads/ directory exists
	info, err := os.Stat(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to stat .beads directory: %w", err)
	}

	// Ensure .beads directory has exactly 0700 permissions (owner rwx only)
	expectedDirMode := os.FileMode(0700)
	if info.Mode().Perm() != expectedDirMode {
		if err := os.Chmod(beadsDir, expectedDirMode); err != nil {
			return fmt.Errorf("failed to fix .beads directory permissions: %w", err)
		}
	}

	// Fix permissions on database file if it exists
	dbPath := filepath.Join(beadsDir, "beads.db")
	if dbInfo, err := os.Stat(dbPath); err == nil {
		// Ensure database has exactly 0600 permissions (owner rw only)
		expectedFileMode := os.FileMode(0600)
		currentPerms := dbInfo.Mode().Perm()
		requiredPerms := os.FileMode(0600)

		// Check if we have both read and write for owner
		if currentPerms&requiredPerms != requiredPerms {
			if err := os.Chmod(dbPath, expectedFileMode); err != nil {
				return fmt.Errorf("failed to fix database permissions: %w", err)
			}
		}
	}

	return nil
}
