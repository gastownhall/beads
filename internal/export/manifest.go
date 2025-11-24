package export

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteManifest writes an export manifest alongside the JSONL file
func WriteManifest(jsonlPath string, manifest *Manifest) error {
	// Derive manifest path from JSONL path
	manifestPath := strings.TrimSuffix(jsonlPath, ".jsonl") + ".manifest.json"

	// Marshal manifest
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	// Create temp file for atomic write
	dir := filepath.Dir(manifestPath)
	base := filepath.Base(manifestPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp manifest file: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	// Write manifest
	if _, err := tempFile.Write(data); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	// Close before rename
	_ = tempFile.Close()

	// Atomic replace
	if err := os.Rename(tempPath, manifestPath); err != nil {
		return fmt.Errorf("failed to replace manifest file: %w", err)
	}

	// Set appropriate file permissions (0600: rw-------)
	if err := os.Chmod(manifestPath, 0600); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to set manifest permissions: %v\n", err)
	}

	return nil
}

// NewManifest creates a new export manifest
func NewManifest(policy ErrorPolicy) *Manifest {
	return &Manifest{
		ExportedAt:  time.Now(),
		ErrorPolicy: string(policy),
		Complete:    true, // Will be set to false if any data is missing
	}
}
