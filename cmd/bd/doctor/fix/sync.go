package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DBJSONLSync fixes database-JSONL sync issues by running bd sync --import-only
func DBJSONLSync(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")

	// Check if both database and JSONL exist
	dbPath := filepath.Join(beadsDir, "beads.db")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	beadsJSONLPath := filepath.Join(beadsDir, "beads.jsonl")

	hasDB := false
	if _, err := os.Stat(dbPath); err == nil {
		hasDB = true
	}

	hasJSONL := false
	if _, err := os.Stat(jsonlPath); err == nil {
		hasJSONL = true
	} else if _, err := os.Stat(beadsJSONLPath); err == nil {
		hasJSONL = true
	}

	if !hasDB || !hasJSONL {
		// Nothing to sync
		return nil
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Run bd sync --import-only to import JSONL updates
	cmd := exec.Command(bdBinary, "sync", "--import-only") // #nosec G204 -- bdBinary from validated executable path
	cmd.Dir = path                                          // Set working directory without changing process dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to sync database with JSONL: %w", err)
	}

	return nil
}
