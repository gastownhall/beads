package fix

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/beads/internal/configfile"
)

// Permissions fixes file permission issues in the .beads directory
func Permissions(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")

	// Check if .beads/ directory exists
	// Use Lstat to detect symlinks - we shouldn't chmod symlinked directories
	// as this would change the target's permissions (problematic on NixOS).
	info, err := os.Lstat(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to stat .beads directory: %w", err)
	}

	// Skip permission fixes for symlinked .beads directories (common on NixOS with home-manager)
	if info.Mode()&os.ModeSymlink != 0 {
		return nil // Symlink permissions are not meaningful on Unix
	}

	// Ensure .beads directory has exactly 0700 permissions (owner rwx only)
	expectedDirMode := os.FileMode(0700)
	if info.Mode().Perm() != expectedDirMode {
		if err := os.Chmod(beadsDir, expectedDirMode); err != nil {
			return fmt.Errorf("failed to fix .beads directory permissions: %w", err)
		}
	}

	// Fix permissions on database file/directory if it exists
	// Resolve the actual database path from config (supports both SQLite and Dolt)
	beadsDirResolved := resolveBeadsDir(beadsDir)
	var dbPath string
	if cfg, err := configfile.Load(beadsDirResolved); err == nil && cfg != nil {
		dbPath = cfg.DatabasePath(beadsDirResolved)
	} else {
		dbPath = filepath.Join(beadsDirResolved, "beads.db")
	}

	// Use Lstat to detect symlinks - skip chmod for symlinked database files
	if dbInfo, err := os.Lstat(dbPath); err == nil {
		// Skip permission fixes for symlinked database files (NixOS)
		if dbInfo.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		if dbInfo.IsDir() {
			// Dolt backend: database is a directory, ensure 0700 (owner rwx)
			expectedDirMode := os.FileMode(0700)
			if dbInfo.Mode().Perm() != expectedDirMode {
				if err := os.Chmod(dbPath, expectedDirMode); err != nil {
					return fmt.Errorf("failed to fix database directory permissions: %w", err)
				}
			}
		} else {
			// SQLite backend: database is a file, ensure 0600 (owner rw)
			expectedFileMode := os.FileMode(0600)
			if dbInfo.Mode().Perm() != expectedFileMode {
				if err := os.Chmod(dbPath, expectedFileMode); err != nil {
					return fmt.Errorf("failed to fix database permissions: %w", err)
				}
			}
		}
	}

	return nil
}
