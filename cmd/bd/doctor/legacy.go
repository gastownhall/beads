package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// CheckLegacyJSONLFilename detects if project is using legacy issues.jsonl
// instead of the canonical beads.jsonl filename.
func CheckLegacyJSONLFilename(repoPath string) DoctorCheck {
	beadsDir := filepath.Join(repoPath, ".beads")

	var jsonlFiles []string
	hasIssuesJSON := false

	for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
		jsonlPath := filepath.Join(beadsDir, name)
		if _, err := os.Stat(jsonlPath); err == nil {
			jsonlFiles = append(jsonlFiles, name)
			if name == "issues.jsonl" {
				hasIssuesJSON = true
			}
		}
	}

	if len(jsonlFiles) == 0 {
		return DoctorCheck{
			Name:    "JSONL Files",
			Status:  "ok",
			Message: "No JSONL files found (database-only mode)",
		}
	}

	if len(jsonlFiles) == 1 {
		// Single JSONL file - check if it's the legacy name
		if hasIssuesJSON {
			return DoctorCheck{
				Name:    "JSONL Files",
				Status:  "warning",
				Message: "Using legacy JSONL filename: issues.jsonl",
				Fix:     "Run 'git mv .beads/issues.jsonl .beads/beads.jsonl' to use canonical name (matches beads.db)",
			}
		}
		return DoctorCheck{
			Name:    "JSONL Files",
			Status:  "ok",
			Message: fmt.Sprintf("Using %s", jsonlFiles[0]),
		}
	}

	// Multiple JSONL files found
	return DoctorCheck{
		Name:    "JSONL Files",
		Status:  "warning",
		Message: fmt.Sprintf("Multiple JSONL files found: %s", strings.Join(jsonlFiles, ", ")),
		Fix:     "Run 'git rm .beads/issues.jsonl' to standardize on beads.jsonl (canonical name)",
	}
}
