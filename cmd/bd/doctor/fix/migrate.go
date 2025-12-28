package fix

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// DatabaseVersion fixes database version mismatches by running bd migrate,
// or creates the database from JSONL by running bd init for fresh clones.
func DatabaseVersion(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Check if database exists - if not, run init instead of migrate
	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// No database - this is a fresh clone, run bd init
		fmt.Println("→ No database found, running 'bd init' to hydrate from JSONL...")
		cmd := newBdCmd(bdBinary, "--db", dbPath, "init")
		cmd.Dir = path
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}

		return nil
	}

	// Database exists - run bd migrate
	cmd := newBdCmd(bdBinary, "--db", dbPath, "migrate")
	cmd.Dir = path // Set working directory without changing process dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}

// findJSONLPath returns the path to the JSONL file in the beads directory.
// Returns empty string if no JSONL file exists.
func findJSONLPath(beadsDir string) string {
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlPath); err == nil {
		return jsonlPath
	}

	beadsJSONLPath := filepath.Join(beadsDir, "beads.jsonl")
	if _, err := os.Stat(beadsJSONLPath); err == nil {
		return beadsJSONLPath
	}

	return ""
}

// SchemaCompatibility fixes schema compatibility issues by running bd migrate
func SchemaCompatibility(path string) error {
	return DatabaseVersion(path)
}
