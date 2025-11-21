package fix

import (
	"fmt"
	"os"
	"os/exec"
)

// DatabaseVersion fixes database version mismatches by running bd migrate
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

	// Run bd migrate
	cmd := exec.Command(bdBinary, "migrate") // #nosec G204 -- bdBinary from validated executable path
	cmd.Dir = path                           // Set working directory without changing process dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}

// SchemaCompatibility fixes schema compatibility issues by running bd migrate
func SchemaCompatibility(path string) error {
	return DatabaseVersion(path)
}
