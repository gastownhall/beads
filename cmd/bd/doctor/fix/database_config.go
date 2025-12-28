package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

// DatabaseConfig auto-detects and fixes metadata.json database/JSONL config mismatches.
// This fixes the issue where metadata.json gets recreated with wrong JSONL filename.
func DatabaseConfig(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")

	// Load existing config
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg == nil {
		// No config exists - nothing to fix
		return fmt.Errorf("no metadata.json found")
	}

	fixed := false

	// Never treat system JSONL files as a JSONL export configuration.
	if isSystemJSONLFilename(cfg.JSONLExport) {
		fmt.Printf("  Updating jsonl_export: %s → issues.jsonl\n", cfg.JSONLExport)
		cfg.JSONLExport = "issues.jsonl"
		fixed = true
	}

	// Check if configured JSONL exists
	if cfg.JSONLExport != "" {
		jsonlPath := cfg.JSONLPath(beadsDir)
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			// Config points to non-existent file - try to find actual JSONL
			actualJSONL := findActualJSONLFile(beadsDir)
			if actualJSONL != "" {
				fmt.Printf("  Updating jsonl_export: %s → %s\n", cfg.JSONLExport, actualJSONL)
				cfg.JSONLExport = actualJSONL
				fixed = true
			}
		}
	}

	// Check if configured database exists
	if cfg.Database != "" {
		dbPath := cfg.DatabasePath(beadsDir)
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			// Config points to non-existent file - try to find actual database
			actualDB := findActualDBFile(beadsDir)
			if actualDB != "" {
				fmt.Printf("  Updating database: %s → %s\n", cfg.Database, actualDB)
				cfg.Database = actualDB
				fixed = true
			}
		}
	}

	if !fixed {
		return fmt.Errorf("no configuration mismatches detected")
	}

	// Save updated config
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// findActualJSONLFile scans .beads/ for the actual JSONL file in use.
// Prefers issues.jsonl over beads.jsonl (canonical name), skips backups and merge artifacts.
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
			strings.HasPrefix(lowerName, "backup_") ||
			// System files are not JSONL exports.
			name == "deletions.jsonl" ||
			name == "interactions.jsonl" ||
			name == "molecules.jsonl" ||
			// Git merge conflict artifacts (e.g., issues.base.jsonl, issues.left.jsonl)
			strings.Contains(lowerName, ".base.jsonl") ||
			strings.Contains(lowerName, ".left.jsonl") ||
			strings.Contains(lowerName, ".right.jsonl") {
			continue
		}

		candidates = append(candidates, name)
	}

	if len(candidates) == 0 {
		return ""
	}

	// Prefer issues.jsonl over beads.jsonl (canonical name)
	for _, name := range candidates {
		if name == "issues.jsonl" {
			return name
		}
	}

	// Fall back to first candidate (including beads.jsonl as legacy)
	return candidates[0]
}

func isSystemJSONLFilename(name string) bool {
	switch name {
	case "deletions.jsonl", "interactions.jsonl", "molecules.jsonl":
		return true
	default:
		return false
	}
}

// LegacyJSONLConfig migrates from legacy beads.jsonl to canonical issues.jsonl.
// This renames the file, updates metadata.json, and updates .gitattributes if present.
func LegacyJSONLConfig(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")

	// Load existing config
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("no metadata.json found")
	}

	legacyPath := filepath.Join(beadsDir, "beads.jsonl")
	canonicalPath := filepath.Join(beadsDir, "issues.jsonl")

	legacyExists := false
	if _, err := os.Stat(legacyPath); err == nil {
		legacyExists = true
	}

	canonicalExists := false
	if _, err := os.Stat(canonicalPath); err == nil {
		canonicalExists = true
	}

	// Case 1: Config says beads.jsonl, file exists, issues.jsonl doesn't exist -> rename
	if cfg.JSONLExport == "beads.jsonl" && legacyExists && !canonicalExists {
		fmt.Printf("  Renaming beads.jsonl → issues.jsonl\n")
		if err := os.Rename(legacyPath, canonicalPath); err != nil {
			return fmt.Errorf("failed to rename file: %w", err)
		}
		cfg.JSONLExport = "issues.jsonl"

		// Update .gitattributes if it references beads.jsonl
		gitattrsPath := filepath.Join(path, ".gitattributes")
		// #nosec G304 -- gitattrsPath is constructed from path which is the git root
		if content, err := os.ReadFile(gitattrsPath); err == nil {
			if strings.Contains(string(content), ".beads/beads.jsonl") {
				newContent := strings.ReplaceAll(string(content), ".beads/beads.jsonl", ".beads/issues.jsonl")
				// #nosec G306 -- .gitattributes should be world-readable
				if err := os.WriteFile(gitattrsPath, []byte(newContent), 0644); err != nil {
					fmt.Printf("  Warning: failed to update .gitattributes: %v\n", err)
				} else {
					fmt.Printf("  Updated .gitattributes\n")
				}
			}
		}
	}

	// Case 2: Config says beads.jsonl but issues.jsonl exists -> just update config
	if cfg.JSONLExport == "beads.jsonl" && canonicalExists {
		fmt.Printf("  Updating config: beads.jsonl → issues.jsonl\n")
		cfg.JSONLExport = "issues.jsonl"
	}

	// Save updated config
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("  Updated metadata.json\n")
	return nil
}

// findActualDBFile scans .beads/ for the actual database file in use.
// Prefers beads.db (canonical name), skips backups and vc.db.
func findActualDBFile(beadsDir string) string {
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

		// Must end with .db
		if !strings.HasSuffix(name, ".db") {
			continue
		}

		// Skip backups and vc.db
		if strings.Contains(name, "backup") || name == "vc.db" {
			continue
		}

		candidates = append(candidates, name)
	}

	if len(candidates) == 0 {
		return ""
	}

	// Prefer beads.db (canonical name)
	for _, name := range candidates {
		if name == "beads.db" {
			return name
		}
	}

	// Fall back to first candidate
	return candidates[0]
}
