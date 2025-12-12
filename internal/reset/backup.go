package reset

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// CreateBackup creates a backup of the .beads directory.
// It copies .beads/ to .beads-backup-{timestamp}/ where timestamp is in YYYYMMDD-HHMMSS format.
// File permissions are preserved during the copy.
// Returns the backup path on success, or an error if the backup directory already exists.
func CreateBackup(beadsDir string) (backupPath string, err error) {
	// Generate timestamp in YYYYMMDD-HHMMSS format
	timestamp := time.Now().Format("20060102-150405")

	// Construct backup directory path
	parentDir := filepath.Dir(beadsDir)
	backupPath = filepath.Join(parentDir, fmt.Sprintf(".beads-backup-%s", timestamp))

	// Check if backup directory already exists
	if _, err := os.Stat(backupPath); err == nil {
		return "", fmt.Errorf("backup directory already exists: %s", backupPath)
	}

	// Create backup directory
	if err := os.Mkdir(backupPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Copy directory recursively
	if err := copyDir(beadsDir, backupPath); err != nil {
		// Attempt to clean up partial backup on failure
		_ = os.RemoveAll(backupPath)
		return "", fmt.Errorf("failed to copy directory: %w", err)
	}

	return backupPath, nil
}

// copyDir recursively copies a directory tree, preserving file permissions
func copyDir(src, dst string) error {
	// Walk the source directory
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		// Construct destination path
		dstPath := filepath.Join(dst, relPath)

		// Handle directories and files
		if info.IsDir() {
			// Skip the root directory (already created)
			if path == src {
				return nil
			}
			// Create directory with same permissions
			return os.Mkdir(dstPath, info.Mode())
		}

		// Copy file
		return copyFile(path, dstPath, info.Mode())
	})

	return err
}

// copyFile copies a single file, preserving permissions
func copyFile(src, dst string, perm os.FileMode) error {
	// #nosec G304 -- backup function only copies files within user's project
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Create destination file with preserved permissions
	// #nosec G304 -- backup function only writes files within user's project
	destFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer destFile.Close()

	// Copy contents
	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	// Ensure data is written to disk
	return destFile.Sync()
}
