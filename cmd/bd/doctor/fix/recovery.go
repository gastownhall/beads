package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DatabaseCorruptionRecovery recovers a corrupted database from JSONL backup.
// It backs up the corrupted database, deletes it, and re-imports from JSONL.
func DatabaseCorruptionRecovery(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, "beads.db")

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("no database to recover")
	}

	// Find JSONL file
	jsonlPath := findJSONLPath(beadsDir)
	if jsonlPath == "" {
		return fmt.Errorf("no JSONL backup found - cannot recover (try restoring from git history)")
	}

	// Count issues in JSONL
	issueCount, err := countJSONLIssues(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}
	if issueCount == 0 {
		return fmt.Errorf("JSONL is empty - cannot recover (try restoring from git history)")
	}

	// Backup corrupted database
	backupPath := dbPath + ".corrupt"
	fmt.Printf("  Backing up corrupted database to %s\n", filepath.Base(backupPath))
	if err := os.Rename(dbPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup corrupted database: %w", err)
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		// Restore corrupted database on failure
		_ = os.Rename(backupPath, dbPath)
		return err
	}

	// Run bd import with --rename-on-import to handle prefix mismatches
	fmt.Printf("  Recovering %d issues from %s\n", issueCount, filepath.Base(jsonlPath))
	cmd := exec.Command(bdBinary, "import", "-i", jsonlPath, "--rename-on-import") // #nosec G204
	cmd.Dir = path
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Keep backup on failure
		fmt.Printf("  Warning: recovery failed, corrupted database preserved at %s\n", filepath.Base(backupPath))
		return fmt.Errorf("failed to import from JSONL: %w", err)
	}

	// Run migrate to set version metadata
	migrateCmd := exec.Command(bdBinary, "migrate") // #nosec G204
	migrateCmd.Dir = path
	migrateCmd.Stdout = os.Stdout
	migrateCmd.Stderr = os.Stderr
	if err := migrateCmd.Run(); err != nil {
		// Non-fatal - import succeeded, version just won't be set
		fmt.Printf("  Warning: migration failed (non-fatal): %v\n", err)
	}

	fmt.Printf("  Recovered %d issues from JSONL backup\n", issueCount)
	return nil
}
