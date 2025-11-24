package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

// CheckLegacyBeadsSlashCommands detects old /beads:* slash commands in documentation
// and recommends migration to bd prime hooks for better token efficiency.
//
// Old pattern: /beads:quickstart, /beads:ready (~10.5k tokens per session)
// New pattern: bd prime hooks (~50-2k tokens per session)
func CheckLegacyBeadsSlashCommands(repoPath string) DoctorCheck {
	docFiles := []string{
		filepath.Join(repoPath, "AGENTS.md"),
		filepath.Join(repoPath, "CLAUDE.md"),
		filepath.Join(repoPath, ".claude", "CLAUDE.md"),
	}

	var filesWithLegacyCommands []string
	legacyPattern := "/beads:"

	for _, docFile := range docFiles {
		content, err := os.ReadFile(docFile) // #nosec G304 - controlled paths from repoPath
		if err != nil {
			continue // File doesn't exist or can't be read
		}

		if strings.Contains(string(content), legacyPattern) {
			filesWithLegacyCommands = append(filesWithLegacyCommands, filepath.Base(docFile))
		}
	}

	if len(filesWithLegacyCommands) == 0 {
		return DoctorCheck{
			Name:    "Documentation",
			Status:  "ok",
			Message: "No legacy beads slash commands detected",
		}
	}

	return DoctorCheck{
		Name:    "Integration Pattern",
		Status:  "warning",
		Message: fmt.Sprintf("Old beads integration detected in %s", strings.Join(filesWithLegacyCommands, ", ")),
		Detail:  "Found: /beads:* slash command references (deprecated)\n" +
			"  These commands are token-inefficient (~10.5k tokens per session)",
		Fix: "Migrate to bd prime hooks for better token efficiency:\n" +
			"\n" +
			"Migration Steps:\n" +
			"  1. Run 'bd setup claude' to add SessionStart/PreCompact hooks\n" +
			"  2. Update AGENTS.md/CLAUDE.md:\n" +
			"     - Remove /beads:* slash command references\n" +
			"     - Add: \"Run 'bd prime' for workflow context\" (for users without hooks)\n" +
			"\n" +
			"Benefits:\n" +
			"  • MCP mode: ~50 tokens vs ~10.5k for full MCP scan (99% reduction)\n" +
			"  • CLI mode: ~1-2k tokens with automatic context recovery\n" +
			"  • Hooks auto-refresh context on session start and before compaction\n" +
			"\n" +
			"See: bd setup claude --help",
	}
}

// CheckAgentDocumentation checks if agent documentation (AGENTS.md or CLAUDE.md) exists
// and recommends adding it if missing, suggesting bd onboard or bd setup claude.
func CheckAgentDocumentation(repoPath string) DoctorCheck {
	docFiles := []string{
		filepath.Join(repoPath, "AGENTS.md"),
		filepath.Join(repoPath, "CLAUDE.md"),
		filepath.Join(repoPath, ".claude", "CLAUDE.md"),
	}

	var foundDocs []string
	for _, docFile := range docFiles {
		if _, err := os.Stat(docFile); err == nil {
			foundDocs = append(foundDocs, filepath.Base(docFile))
		}
	}

	if len(foundDocs) > 0 {
		return DoctorCheck{
			Name:    "Agent Documentation",
			Status:  "ok",
			Message: fmt.Sprintf("Documentation found: %s", strings.Join(foundDocs, ", ")),
		}
	}

	return DoctorCheck{
		Name:    "Agent Documentation",
		Status:  "warning",
		Message: "No agent documentation found",
		Detail:  "Missing: AGENTS.md or CLAUDE.md\n" +
			"  Documenting workflow helps AI agents work more effectively",
		Fix: "Add agent documentation:\n" +
			"  • Run 'bd onboard' to create AGENTS.md with workflow guidance\n" +
			"  • Or run 'bd setup claude' to add Claude-specific documentation\n" +
			"\n" +
			"Recommended: Include bd workflow in your project documentation so\n" +
			"AI agents understand how to track issues and manage dependencies",
	}
}

// CheckLegacyJSONLFilename detects if there are multiple JSONL files,
// which can cause sync/merge issues. Ignores merge artifacts and backups.
func CheckLegacyJSONLFilename(repoPath string) DoctorCheck {
	beadsDir := filepath.Join(repoPath, ".beads")

	// Find all .jsonl files
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "JSONL Files",
			Status:  "ok",
			Message: "No .beads directory found",
		}
	}

	var realJSONLFiles []string
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
			strings.HasPrefix(lowerName, "backup_") {
			continue
		}

		realJSONLFiles = append(realJSONLFiles, name)
	}

	if len(realJSONLFiles) == 0 {
		return DoctorCheck{
			Name:    "JSONL Files",
			Status:  "ok",
			Message: "No JSONL files found (database-only mode)",
		}
	}

	if len(realJSONLFiles) == 1 {
		return DoctorCheck{
			Name:    "JSONL Files",
			Status:  "ok",
			Message: fmt.Sprintf("Using %s", realJSONLFiles[0]),
		}
	}

	// Multiple JSONL files found - this is a problem!
	return DoctorCheck{
		Name:    "JSONL Files",
		Status:  "warning",
		Message: fmt.Sprintf("Multiple JSONL files found: %s", strings.Join(realJSONLFiles, ", ")),
		Detail:  "Having multiple JSONL files can cause sync and merge conflicts.\n" +
			"  Only one JSONL file should be used per repository.",
		Fix: "Determine which file is current and remove the others:\n" +
			"  1. Check 'bd stats' to see which file is being used\n" +
			"  2. Verify with 'git log .beads/*.jsonl' to see commit history\n" +
			"  3. Remove the unused file(s): git rm .beads/<unused>.jsonl\n" +
			"  4. Commit the change",
	}
}

// CheckDatabaseConfig verifies that the configured database and JSONL paths
// match what actually exists on disk.
func CheckDatabaseConfig(repoPath string) DoctorCheck {
	beadsDir := filepath.Join(repoPath, ".beads")

	// Load config
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		// No config or error reading - use defaults
		return DoctorCheck{
			Name:    "Database Config",
			Status:  "ok",
			Message: "Using default configuration",
		}
	}

	var issues []string

	// Check if configured database exists
	if cfg.Database != "" {
		dbPath := cfg.DatabasePath(beadsDir)
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			// Check if other .db files exist
			entries, _ := os.ReadDir(beadsDir)
			var otherDBs []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".db") {
					otherDBs = append(otherDBs, entry.Name())
				}
			}
			if len(otherDBs) > 0 {
				issues = append(issues, fmt.Sprintf("Configured database '%s' not found, but found: %s",
					cfg.Database, strings.Join(otherDBs, ", ")))
			}
		}
	}

	// Check if configured JSONL exists
	if cfg.JSONLExport != "" {
		jsonlPath := cfg.JSONLPath(beadsDir)
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			// Check if other .jsonl files exist
			entries, _ := os.ReadDir(beadsDir)
			var otherJSONLs []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
					name := entry.Name()
					// Skip backups
					lowerName := strings.ToLower(name)
					if !strings.Contains(lowerName, "backup") &&
						!strings.Contains(lowerName, ".orig") &&
						!strings.Contains(lowerName, ".bak") {
						otherJSONLs = append(otherJSONLs, name)
					}
				}
			}
			if len(otherJSONLs) > 0 {
				issues = append(issues, fmt.Sprintf("Configured JSONL '%s' not found, but found: %s",
					cfg.JSONLExport, strings.Join(otherJSONLs, ", ")))
			}
		}
	}

	if len(issues) == 0 {
		return DoctorCheck{
			Name:    "Database Config",
			Status:  "ok",
			Message: "Configuration matches existing files",
		}
	}

	return DoctorCheck{
		Name:    "Database Config",
		Status:  "warning",
		Message: "Configuration mismatch detected",
		Detail:  strings.Join(issues, "\n  "),
		Fix: "Update configuration in .beads/metadata.json:\n" +
			"  1. Check which files are actually being used\n" +
			"  2. Update metadata.json to match the actual filenames\n" +
			"  3. Or rename the files to match the configuration",
	}
}
